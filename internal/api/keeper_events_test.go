package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Append-only keeper decision ledger (#1369).
//
// keeper_requests is written PENDING and then UPDATEd in place to its decision.
// After that update the row says only "ALLOW" — there is no record that the
// request was ever pending, when it was raised versus decided, or that the
// decision was rewritten. These tests pin that every transition now survives.

func keeperEventsLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// newKeeperTestJournal wires a REAL journal writer (not the noop emitter) so the
// entries actually land in journal_entries and pick up the hash-chain columns —
// the whole point of mirroring keeper decisions there. Emit only QUEUES, so the
// caller must Flush before reading the rows back.
func newKeeperTestJournal(t *testing.T, db *sql.DB) *journal.Writer {
	t.Helper()
	w := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	t.Cleanup(func() { _ = w.Close() })
	return w
}

type ledgerEvent struct {
	Seq       int
	State     string
	ActorType string
	Reason    string
	RiskScore sql.NullInt64
	ExitCode  sql.NullInt64
	Workspace sql.NullString
	Command   sql.NullString
}

// readLedger returns every transition recorded for a request, in seq order.
func readLedger(t *testing.T, db *sql.DB, requestID string) []ledgerEvent {
	t.Helper()
	rows, err := db.Query(`
		SELECT seq, state, actor_type, COALESCE(reason,''), risk_score, exit_code, workspace_id, command
		  FROM keeper_request_events WHERE request_id = ? ORDER BY seq`, requestID)
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer rows.Close()
	var out []ledgerEvent
	for rows.Next() {
		var e ledgerEvent
		if err := rows.Scan(&e.Seq, &e.State, &e.ActorType, &e.Reason,
			&e.RiskScore, &e.ExitCode, &e.Workspace, &e.Command); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows ledger: %v", err)
	}
	return out
}

// latestRequestID returns the id of the single keeper_requests row, so a test can
// find the request the handler generated internally.
func latestRequestID(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM keeper_requests ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read request id: %v", err)
	}
	return id
}

// TestKeeperRequest_RecordsPendingThenDecision is the core of #1369 item 4: the
// PENDING state must survive the transition to a decision. Before this change the
// in-place UPDATE overwrote it and the prior state was simply gone.
func TestKeeperRequest_RecordsPendingThenDecision(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 3,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: %d %s", w.Code, w.Body.String())
	}

	reqID := latestRequestID(t, db)
	events := readLedger(t, db, reqID)
	if len(events) != 2 {
		t.Fatalf("ledger has %d events (%+v), want 2 (PENDING then ALLOW)", len(events), events)
	}
	if events[0].Seq != 1 || events[0].State != keeperStatePending {
		t.Errorf("event 1 = seq %d state %q, want seq 1 PENDING", events[0].Seq, events[0].State)
	}
	if events[1].Seq != 2 || events[1].State != string(keeper.DecisionAllow) {
		t.Errorf("event 2 = seq %d state %q, want seq 2 ALLOW", events[1].Seq, events[1].State)
	}
	// The PENDING transition is caused by the agent asking; the decision by the
	// gatekeeper. A ledger that cannot distinguish them cannot answer "who".
	if events[0].ActorType != keeperActorAgent {
		t.Errorf("PENDING actor_type = %q, want %q", events[0].ActorType, keeperActorAgent)
	}
	if events[1].ActorType != keeperActorKeeper {
		t.Errorf("decision actor_type = %q, want %q", events[1].ActorType, keeperActorKeeper)
	}
	// A PENDING has no risk score yet — NULL, not a misleading 0.
	if events[0].RiskScore.Valid {
		t.Errorf("PENDING risk_score = %v, want NULL", events[0].RiskScore.Int64)
	}
	if !events[1].RiskScore.Valid || events[1].RiskScore.Int64 != 3 {
		t.Errorf("decision risk_score = %v, want 3", events[1].RiskScore)
	}
	if events[0].Workspace.String != wsID {
		t.Errorf("workspace_id = %q, want %q", events[0].Workspace.String, wsID)
	}
}

// TestKeeperRequest_DenyRecordsTransition: a DENY is as much a decision as an
// ALLOW and must leave the same two-row history.
func TestKeeperRequest_DenyRecordsTransition(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "nope", RiskScore: 9,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: %d %s", w.Code, w.Body.String())
	}

	events := readLedger(t, db, latestRequestID(t, db))
	if len(events) != 2 || events[1].State != string(keeper.DecisionDeny) {
		t.Fatalf("events = %+v, want PENDING then DENY", events)
	}
	if events[1].Reason != "nope" {
		t.Errorf("decision reason = %q, want %q", events[1].Reason, "nope")
	}
}

// TestKeeperExecute_RecordsPendingThenAllowWithExitCode covers the highest-stakes
// path. The exit code lands on the ALLOW transition, so the ledger records what
// the command actually did — not just that it was permitted.
func TestKeeperExecute_RecordsPendingThenAllowWithExitCode(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(&mockContainerExec{output: "done", exitCode: 0, execID: "e1"})

	if w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "list PRs", Command: "gh pr list",
		ContainerID: "test-container",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper execute: %d %s", w.Code, w.Body.String())
	}

	events := readLedger(t, db, latestRequestID(t, db))
	if len(events) != 2 {
		t.Fatalf("ledger has %d events (%+v), want 2", len(events), events)
	}
	if events[0].State != keeperStatePending || events[1].State != string(keeper.DecisionAllow) {
		t.Fatalf("states = %q,%q, want PENDING,ALLOW", events[0].State, events[1].State)
	}
	// The command is denormalised onto the ledger so the record stays
	// self-describing if keeper_requests is ever pruned.
	if events[1].Command.String != "gh pr list" {
		t.Errorf("command = %q, want %q", events[1].Command.String, "gh pr list")
	}
	if !events[1].ExitCode.Valid || events[1].ExitCode.Int64 != 0 {
		t.Errorf("exit_code = %v, want 0", events[1].ExitCode)
	}
	// PENDING precedes the run, so it cannot carry an exit code.
	if events[0].ExitCode.Valid {
		t.Errorf("PENDING exit_code = %v, want NULL", events[0].ExitCode.Int64)
	}
}

// TestKeeperExecute_DuplicateSuppressedRecordsTerminalEvent: a suppressed
// duplicate is a real audit event (someone tried to re-run a credential-bound
// command) and needs a ledger row of its own, attributed to the system rather
// than to the gatekeeper — nothing was evaluated.
func TestKeeperExecute_DuplicateSuppressedRecordsTerminalEvent(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(&mockContainerExec{output: "done", exitCode: 0, execID: "e1"})

	body := keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "list PRs", Command: "gh pr list",
		ContainerID: "test-container",
	}
	if w := doKeeperExecute(h, body); w.Code != http.StatusOK {
		t.Fatalf("first execute: %d %s", w.Code, w.Body.String())
	}
	// Immediately repeat: the dedup debounce window is still open.
	if w := doKeeperExecute(h, body); w.Code != http.StatusConflict {
		t.Fatalf("duplicate execute: got %d, want 409", w.Code)
	}

	var dupID string
	if err := db.QueryRow(
		`SELECT id FROM keeper_requests WHERE decision = 'DUPLICATE_SUPPRESSED'`).Scan(&dupID); err != nil {
		t.Fatalf("find suppressed request: %v", err)
	}
	events := readLedger(t, db, dupID)
	if len(events) != 1 {
		t.Fatalf("suppressed request has %d events (%+v), want 1 terminal event", len(events), events)
	}
	if events[0].State != "DUPLICATE_SUPPRESSED" {
		t.Errorf("state = %q, want DUPLICATE_SUPPRESSED", events[0].State)
	}
	if events[0].ActorType != keeperActorSystem {
		t.Errorf("actor_type = %q, want %q (nothing was evaluated)", events[0].ActorType, keeperActorSystem)
	}
}

// TestKeeperLedger_ProjectionAndHistoryAgree: the ledger's final transition must
// match what keeper_requests reports. A divergent audit trail is worse than an
// absent one, because you cannot tell which half lied — which is why every write
// happens in one transaction.
func TestKeeperLedger_ProjectionAndHistoryAgree(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionEscalate), Reason: "human needed", RiskScore: 6,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: %d %s", w.Code, w.Body.String())
	}

	reqID := latestRequestID(t, db)
	var projection string
	if err := db.QueryRow(`SELECT decision FROM keeper_requests WHERE id = ?`, reqID).Scan(&projection); err != nil {
		t.Fatalf("read projection: %v", err)
	}
	events := readLedger(t, db, reqID)
	if len(events) == 0 {
		t.Fatal("no ledger events")
	}
	if last := events[len(events)-1].State; last != projection {
		t.Fatalf("ledger tail %q != keeper_requests.decision %q", last, projection)
	}
}

// TestKeeperLedger_IsAppendOnlyAtRuntime proves the DB trigger, not just the
// handler discipline, is what makes the ledger immutable — the guarantee has to
// survive a future caller who reaches for an UPDATE.
func TestKeeperLedger_IsAppendOnlyAtRuntime(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: %d %s", w.Code, w.Body.String())
	}

	reqID := latestRequestID(t, db)
	if _, err := db.Exec(
		`UPDATE keeper_request_events SET state = 'ALLOW' WHERE request_id = ? AND seq = 1`, reqID); err == nil {
		t.Fatal("rewriting a recorded transition succeeded — the ledger is not append-only")
	}

	// And the original PENDING is still there.
	events := readLedger(t, db, reqID)
	if len(events) == 0 || events[0].State != keeperStatePending {
		t.Fatalf("events = %+v, want the PENDING transition intact", events)
	}
}

// TestKeeperExecute_MirrorsDecisionIntoJournal: /keeper/execute previously emitted
// NOTHING to the journal — only a WebSocket broadcast and the mutable
// keeper_requests row. The journal is the only store with a keyed tamper-evident
// hash-chain, so the most sensitive keeper decision class was the one class with
// no tamper-evidence at all.
func TestKeeperExecute_MirrorsDecisionIntoJournal(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(&mockContainerExec{output: "done", exitCode: 0, execID: "e1"})
	jw := newKeeperTestJournal(t, db)
	h.SetJournal(jw)

	if w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "list PRs", Command: "gh pr list",
		ContainerID: "test-container",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper execute: %d %s", w.Code, w.Body.String())
	}

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}

	var n int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM journal_entries
		 WHERE entry_type = 'keeper.decision'
		   AND json_extract(payload, '$.request_type') = 'execute'`).Scan(&n); err != nil {
		t.Fatalf("count journal entries: %v", err)
	}
	if n != 1 {
		t.Fatalf("keeper.decision journal entries for execute = %d, want 1 — the execute path must inherit the hash-chain", n)
	}

	// The chained entry must be self-sufficient: it carries the exit code and the
	// command, so it stays meaningful if keeper_requests is later pruned.
	var exit, cmd string
	if err := db.QueryRow(`
		SELECT COALESCE(json_extract(payload, '$.exit_code'), ''),
		       COALESCE(json_extract(payload, '$.command'), '')
		  FROM journal_entries WHERE entry_type = 'keeper.decision'`).Scan(&exit, &cmd); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if exit != "0" {
		t.Errorf("payload.exit_code = %q, want 0", exit)
	}
	if cmd != "gh pr list" {
		t.Errorf("payload.command = %q, want the executed command", cmd)
	}
}

// TestKeeperExecute_DenyMirrorsIntoJournalAsWarn: a denied credential-bound
// command is the event an operator most wants surfaced, so it must be chained AND
// severity-escalated the way the /keeper/request path already does.
func TestKeeperExecute_DenyMirrorsIntoJournalAsWarn(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "exfil shaped", RiskScore: 9,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(&mockContainerExec{output: "", exitCode: 0, execID: "e1"})
	jw := newKeeperTestJournal(t, db)
	h.SetJournal(jw)

	if w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "exfil", Command: "gh pr list",
		ContainerID: "test-container",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper execute: %d %s", w.Code, w.Body.String())
	}

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}

	var severity string
	if err := db.QueryRow(
		`SELECT severity FROM journal_entries WHERE entry_type = 'keeper.decision'`).Scan(&severity); err != nil {
		t.Fatalf("read severity: %v", err)
	}
	if severity != "warn" {
		t.Errorf("DENY journal severity = %q, want warn", severity)
	}
}

// TestKeeperRequestWorkspace_FallsBackToCrew guards a real invisibility bug: two
// Phase-2 evaluators (skill_review, memory_health) are crew-scoped and pass an
// EMPTY agent id. Agent-only workspace resolution would leave their verdicts with
// a NULL workspace and therefore invisible to the workspace-scoped events
// endpoint — recorded but unreadable, which is not much better than unrecorded.
func TestKeeperRequestWorkspace_FallsBackToCrew(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)
	ctx := context.Background()
	logger := keeperEventsLogger()

	if got := keeperRequestWorkspace(ctx, db, logger, agentID, ""); got != wsID {
		t.Errorf("from agent: got %q, want %q", got, wsID)
	}
	if got := keeperRequestWorkspace(ctx, db, logger, "", crewID); got != wsID {
		t.Errorf("from crew (agent-less Phase-2 sweep): got %q, want %q", got, wsID)
	}
	// An unknown agent must still fall through to the crew rather than giving up.
	if got := keeperRequestWorkspace(ctx, db, logger, "no-such-agent", crewID); got != wsID {
		t.Errorf("unknown agent + known crew: got %q, want %q", got, wsID)
	}
	// Neither resolvable → empty, which the schema stores as NULL. Never an error:
	// the transition matters more than the tenant tag.
	if got := keeperRequestWorkspace(ctx, db, logger, "", ""); got != "" {
		t.Errorf("nothing to resolve: got %q, want empty", got)
	}
}

// TestKeeperPhase2_RecordsTerminalTransition covers the fourth write site. A
// Phase-2 verdict is written already-decided (no PENDING window), so it must land
// as exactly one terminal transition — and it must be readable per-tenant even
// though the evaluator passed no agent id.
func TestKeeperPhase2_RecordsTerminalTransition(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, _, _ := seedKeeperFixture(t, db)

	// Exercise the shared recorder directly: the four F4 endpoints differ only in
	// which evaluator produced the verdict, and all funnel through this one call.
	h := &KeeperPhase2Handler{db: db, logger: keeperEventsLogger()}
	reqID, err := h.recordKeeperRequest(context.Background(),
		keeper.RequestTypeMemoryHealth,
		"", crewID, "F4.3 daily memory health sweep",
		string(keeper.DecisionAllow), "looks healthy", 3, "prompt", "raw")
	if err != nil {
		t.Fatalf("recordKeeperRequest: %v", err)
	}

	events := readLedger(t, db, reqID)
	if len(events) != 1 {
		t.Fatalf("Phase-2 verdict produced %d transitions (%+v), want 1 terminal event", len(events), events)
	}
	if events[0].State != string(keeper.DecisionAllow) {
		t.Errorf("state = %q, want ALLOW", events[0].State)
	}
	if events[0].Workspace.String != wsID {
		t.Errorf("workspace_id = %q, want %q resolved from the crew", events[0].Workspace.String, wsID)
	}
}

// TestKeeperPhase2_EmptyDecisionRecordsPending: an evaluator that returns no
// verdict previously wrote the row with a NULL decision and SUCCEEDED. Making the
// ledger reject that would be an availability regression smuggled in with an audit
// change, so it records PENDING instead.
func TestKeeperPhase2_EmptyDecisionRecordsPending(t *testing.T) {
	db := setupTestDB(t)
	_, crewID, _, _ := seedKeeperFixture(t, db)

	h := &KeeperPhase2Handler{db: db, logger: keeperEventsLogger()}
	reqID, err := h.recordKeeperRequest(context.Background(),
		keeper.RequestTypeSkillReview,
		"", crewID, "F4.1 skill review", "" /* no decision */, "", 1, "", "")
	if err != nil {
		t.Fatalf("an empty decision must not fail the record: %v", err)
	}
	events := readLedger(t, db, reqID)
	if len(events) != 1 || events[0].State != keeperStatePending {
		t.Fatalf("events = %+v, want a single PENDING transition", events)
	}
}

// TestKeeperExecute_AuditSurvivesClientDisconnect: by the time the ALLOW audit is
// written the secret has been injected and the command HAS RUN. If the audit rode
// r.Context() and the agent (or a proxy) disconnected mid-exec, the transaction
// would abort and the ALLOW would be recorded nowhere but the container's exec
// log — making a disconnect a way to execute with a credential and leave no trail.
//
// The post-exec writes therefore use a detached, bounded context.
func TestKeeperExecute_AuditSurvivesClientDisconnect(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, keeperEventsLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(&mockContainerExec{output: "done", exitCode: 0, execID: "e1"})
	jw := newKeeperTestJournal(t, db)
	h.SetJournal(jw)

	// A request whose context is ALREADY cancelled — the state the handler is in
	// when the client hangs up while the container command is running.
	body := keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "list PRs", Command: "gh pr list",
		ContainerID: "test-container",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/execute", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	// The container mock cancels the request the moment Exec is entered — i.e.
	// after the gatekeeper ALLOW and the secret injection, and before the audit
	// write. That is the real disconnect-mid-exec ordering.
	h.WithContainer(&cancelOnExecContainer{
		mockContainerExec: &mockContainerExec{output: "done", exitCode: 0, execID: "e1"},
		cancel:            cancel,
	})
	h.HandleExecute(w, req)

	if ctx.Err() == nil {
		t.Fatal("fixture did not cancel the request context — the test would not exercise the disconnect")
	}

	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}

	// The decision must be recorded despite the dead request context.
	reqID := latestRequestID(t, db)
	events := readLedger(t, db, reqID)
	if len(events) < 2 {
		t.Fatalf("ledger has %d transitions (%+v) after a mid-exec disconnect — the ALLOW was lost",
			len(events), events)
	}
	if last := events[len(events)-1].State; last != string(keeper.DecisionAllow) {
		t.Errorf("ledger tail = %q, want ALLOW", last)
	}

	var decision string
	if err := db.QueryRow(`SELECT COALESCE(decision,'') FROM keeper_requests WHERE id = ?`, reqID).Scan(&decision); err != nil {
		t.Fatalf("read projection: %v", err)
	}
	if decision != string(keeper.DecisionAllow) {
		t.Errorf("keeper_requests.decision = %q, want ALLOW", decision)
	}

	var journaled int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM journal_entries
		 WHERE entry_type = 'keeper.decision'
		   AND json_extract(payload, '$.request_type') = 'execute'`).Scan(&journaled); err != nil {
		t.Fatalf("count journal entries: %v", err)
	}
	if journaled != 1 {
		t.Errorf("keeper.decision journal entries = %d, want 1 (the chained record must survive the disconnect)", journaled)
	}
}

// cancelOnExecContainer cancels the in-flight request as soon as the container
// command starts, reproducing a client that hangs up mid-exec.
type cancelOnExecContainer struct {
	*mockContainerExec
	cancel context.CancelFunc
}

func (c *cancelOnExecContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.cancel()
	return c.mockContainerExec.Exec(ctx, cfg)
}
