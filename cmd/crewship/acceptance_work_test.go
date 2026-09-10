package main

// Acceptance for `crewship work`, driven through the BUILT BINARY against a
// REAL api.Router over a REAL migrated database.
//
// Stub-based CLI tests have shipped commands that were broken on every real
// server, because a stub can only confirm the CLI's own belief about the wire.
// So nothing here is faked: the router is the one cmd_start.go builds, the
// ledger rows are the ones internal/work writes, and the CLI is a subprocess.
//
// The assertions that matter are the ones about honesty:
//   - a cancel against a live runtime must NOT print "cancelled";
//   - a replay must mint a new id under the caller's identity;
//   - a replay whose payload expired must refuse and say why;
//   - no command may print the accepted input or a webhook payload.

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

const workAcceptanceWorkspaceID = "cworkws0000000000001"

// The payload strings the ledger holds and the CLI must never print.
const (
	workAcceptanceChatSecret    = "PRIVATE-CHAT-TEXT-DO-NOT-PRINT"
	workAcceptanceWebhookSecret = "ghp_acceptance_should_never_print"
)

func startWorkAcceptanceServer(t *testing.T) string {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Work', 'work-ws')`, workAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('work-owner', 'owner@work-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wkm-owner', ?, 'work-owner', 'OWNER')`,
		workAcceptanceWorkspaceID)

	const ownerToken = "crewship_cli_workowner000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-work-owner', 'work-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	now := tsformat.Format(time.Now().UTC())
	seed := func(id, state, source, sourceRef, agent, input string, terminal bool) {
		t.Helper()
		var terminalAt any
		if terminal {
			terminalAt = now
		}
		mustExec(`INSERT INTO work_items (id, workspace_id, source, source_ref, domain_kind, domain_id,
			agent_id, crew_id, session_id, class, authorized_by_user_id, authorized_scope,
			input_json, input_sha256, target_revision, state, state_reason, generation, attempts,
			priority, eligible_at, created_at, updated_at, terminal_at)
			VALUES (?,?,?,?,'','',?,'','sess-1','background','work-owner','OWNER',?,'sha-acc','rev-1',?,'',2,1,0,?,?,?,?)`,
			id, workAcceptanceWorkspaceID, source, sourceRef, agent, input, state, now, now, now, terminalAt)
		mustExec(`INSERT INTO work_events (work_id, seq, at, from_state, to_state, run_id, generation, reason)
			VALUES (?,1,?,'','queued','',0,'accepted')`, id, now)
	}

	// A live item (cancel must be honest about it), a queued item (cancel
	// stops it for real), and two finished webhook items — one whose payload
	// is still held and one whose payload expired.
	seed("wk-live-000001", "running", "chat", "", "agent-a", `{"message":"`+workAcceptanceChatSecret+`"}`, false)
	seed("wk-queued-0001", "queued", "manual", "", "agent-a", `{}`, false)
	seed("wk-replay-0001", "failed", "webhook", "dlv-held-00001", "agent-b", `{"payload":"kept"}`, true)
	seed("wk-expired-001", "failed", "webhook", "dlv-expired-01", "agent-b", `{"payload":"gone"}`, true)

	mustExec(`INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner, lease_expires_at,
		heartbeat_at, runtime_locator, started_at, start_reason, cost_usd)
		VALUES ('run-acc-1','wk-live-000001',1,2,'worker-a',?,?,'container:acc',?,'claimed',1.5)`, now, now, now)

	delivery := func(id, sourceID, workID string, raw []byte, expires any) {
		t.Helper()
		mustExec(`INSERT INTO webhook_deliveries (id, workspace_id, endpoint_id, endpoint_kind, profile,
			source_delivery_id, content_key, body_sha256, body_bytes, raw_body, raw_body_expires_at,
			event_type, event_action, signing_key_id, filter_decision, filter_reason, target_revision,
			work_id, received_at, dedup_expires_at)
			VALUES (?,?,'ep-acc','agent','github',?,'','sha-body',64,?,?,'issues','opened','key-1','accepted','','rev-1',?,?,?)`,
			id, workAcceptanceWorkspaceID, sourceID, raw, expires, workID, now, now)
	}
	delivery("dlv-held-00001", "src-held", "wk-replay-0001",
		[]byte(`{"token":"`+workAcceptanceWebhookSecret+`"}`), nil)
	delivery("dlv-expired-01", "src-expired", "wk-expired-001",
		nil, tsformat.Format(time.Now().UTC().Add(-24*time.Hour)))
	mustExec(`INSERT INTO webhook_deliveries (id, workspace_id, endpoint_id, endpoint_kind, profile,
		source_delivery_id, content_key, body_sha256, body_bytes, event_type, event_action, signing_key_id,
		filter_decision, filter_reason, target_revision, received_at, dedup_expires_at)
		VALUES ('dlv-ignored-01',?,'ep-acc','agent','github','src-ignored','','sha-ping',12,'ping','','key-1',
		'ignored','ping is not an event','',?,?)`, workAcceptanceWorkspaceID, now, now)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + workAcceptanceWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runWorkCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAcceptance_WorkListAndGet_ShowTheLedgerWithoutThePayload drives the two
// read commands and pins the §9 privacy rule end to end: the accepted input is
// in the database the CLI just read from, and must not appear in its output.
func TestAcceptance_WorkListAndGet_ShowTheLedgerWithoutThePayload(t *testing.T) {
	cfgPath := startWorkAcceptanceServer(t)

	listOut, err := runWorkCLI(t, cfgPath, "work", "list")
	if err != nil {
		t.Fatalf("work list failed: %v\n%s", err, listOut)
	}
	for _, want := range []string{"wk-live-000001", "wk-queued-0001", "running", "queued"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("work list is missing %q:\n%s", want, listOut)
		}
	}

	filtered, err := runWorkCLI(t, cfgPath, "work", "list", "--state", "running")
	if err != nil {
		t.Fatalf("work list --state failed: %v\n%s", err, filtered)
	}
	if !strings.Contains(filtered, "wk-live-000001") || strings.Contains(filtered, "wk-queued-0001") {
		t.Fatalf("--state running did not filter:\n%s", filtered)
	}

	// A misspelt state is refused rather than answered with an empty page.
	typoOut, err := runWorkCLI(t, cfgPath, "work", "list", "--state", "runnign")
	if err == nil {
		t.Fatalf("expected a misspelt --state to fail, got:\n%s", typoOut)
	}

	getOut, err := runWorkCLI(t, cfgPath, "work", "get", "wk-live-000001")
	if err != nil {
		t.Fatalf("work get failed: %v\n%s", err, getOut)
	}
	for _, want := range []string{"run-acc-1", "container:acc", "accepted", "sha-acc"} {
		if !strings.Contains(getOut, want) {
			t.Fatalf("work get is missing %q:\n%s", want, getOut)
		}
	}
	if strings.Contains(getOut, workAcceptanceChatSecret) {
		t.Fatalf("work get printed the accepted input:\n%s", getOut)
	}
}

// TestAcceptance_WorkCancel_DoesNotClaimWhatItDidNotDo is the §4 assertion,
// driven through the binary an agent actually runs.
func TestAcceptance_WorkCancel_DoesNotClaimWhatItDidNotDo(t *testing.T) {
	cfgPath := startWorkAcceptanceServer(t)

	liveOut, err := runWorkCLI(t, cfgPath, "work", "cancel", "wk-live-000001")
	if err != nil {
		t.Fatalf("work cancel failed: %v\n%s", err, liveOut)
	}
	if !strings.Contains(liveOut, "cancel requested, not yet confirmed") {
		t.Fatalf("cancelling live work must not report a confirmed stop:\n%s", liveOut)
	}
	// The ledger still says running, and the CLI's own read agrees.
	stillRunning, err := runWorkCLI(t, cfgPath, "work", "get", "wk-live-000001")
	if err != nil {
		t.Fatalf("work get after cancel failed: %v\n%s", err, stillRunning)
	}
	if !strings.Contains(stillRunning, "running") {
		t.Fatalf("the item should still be running after a signalled cancel:\n%s", stillRunning)
	}
	if !strings.Contains(stillRunning, "cancel requested") {
		t.Fatalf("the cancel request must be durable in the history:\n%s", stillRunning)
	}

	queuedOut, err := runWorkCLI(t, cfgPath, "work", "cancel", "wk-queued-0001")
	if err != nil {
		t.Fatalf("work cancel (queued) failed: %v\n%s", err, queuedOut)
	}
	if !strings.Contains(queuedOut, "is cancelled") {
		t.Fatalf("queued work should stop atomically:\n%s", queuedOut)
	}
	// Repeating it is safe and still reads as success.
	againOut, err := runWorkCLI(t, cfgPath, "work", "cancel", "wk-queued-0001")
	if err != nil {
		t.Fatalf("repeated cancel failed: %v\n%s", err, againOut)
	}
	if !strings.Contains(againOut, "is cancelled") {
		t.Fatalf("a repeated cancel should be idempotent:\n%s", againOut)
	}
}

// TestAcceptance_WorkReplay_MintsNewWorkAndRefusesWhatItCannotReproduce
// exercises both halves of §4's replay rule through the CLI.
func TestAcceptance_WorkReplay_MintsNewWorkAndRefusesWhatItCannotReproduce(t *testing.T) {
	cfgPath := startWorkAcceptanceServer(t)

	replayOut, err := runWorkCLI(t, cfgPath, "work", "replay", "wk-replay-0001", "--reason", "provider outage")
	if err != nil {
		t.Fatalf("work replay failed: %v\n%s", err, replayOut)
	}
	if !strings.Contains(replayOut, "Replayed wk-replay-0001 as ") {
		t.Fatalf("the replay must name the new work id:\n%s", replayOut)
	}
	const marker = "Replayed wk-replay-0001 as "
	rest := strings.TrimSpace(replayOut[strings.Index(replayOut, marker)+len(marker):])
	newID := strings.Fields(rest)[0]
	if newID == "wk-replay-0001" {
		t.Fatalf("a replay must mint a NEW id: %s", replayOut)
	}

	detail, err := runWorkCLI(t, cfgPath, "work", "get", newID)
	if err != nil {
		t.Fatalf("work get on the replay failed: %v\n%s", err, detail)
	}
	for _, want := range []string{"wk-replay-0001", "provider outage", "queued"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the replay is missing %q:\n%s", want, detail)
		}
	}

	// Live work cannot be replayed.
	liveOut, err := runWorkCLI(t, cfgPath, "work", "replay", "wk-live-000001")
	if err == nil {
		t.Fatalf("expected replaying live work to fail, got:\n%s", liveOut)
	}
	if !strings.Contains(liveOut, "finished") {
		t.Fatalf("the refusal must explain itself:\n%s", liveOut)
	}

	// A payload past its retention refuses, and says so rather than failing
	// on a NULL somewhere downstream.
	expiredOut, err := runWorkCLI(t, cfgPath, "work", "replay", "wk-expired-001")
	if err == nil {
		t.Fatalf("expected an expired payload to refuse the replay, got:\n%s", expiredOut)
	}
	if !strings.Contains(expiredOut, "retention") {
		t.Fatalf("the refusal must name the retention:\n%s", expiredOut)
	}
}

// TestAcceptance_WorkDeliveries_AuditWithoutPublishingThePayload drives the
// delivery ledger commands.
func TestAcceptance_WorkDeliveries_AuditWithoutPublishingThePayload(t *testing.T) {
	cfgPath := startWorkAcceptanceServer(t)

	listOut, err := runWorkCLI(t, cfgPath, "work", "deliveries", "list")
	if err != nil {
		t.Fatalf("work deliveries list failed: %v\n%s", err, listOut)
	}
	for _, want := range []string{"dlv-held-00001", "dlv-expired-01", "dlv-ignored-01", "held", "dropped"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("the delivery list is missing %q:\n%s", want, listOut)
		}
	}

	ignoredOut, err := runWorkCLI(t, cfgPath, "work", "deliveries", "list", "--decision", "ignored")
	if err != nil {
		t.Fatalf("--decision ignored failed: %v\n%s", err, ignoredOut)
	}
	if !strings.Contains(ignoredOut, "dlv-ignored-01") || strings.Contains(ignoredOut, "dlv-held-00001") {
		t.Fatalf("--decision did not filter:\n%s", ignoredOut)
	}

	// "Did you receive this one" — the identity lookup, which needs both.
	lookupOut, err := runWorkCLI(t, cfgPath, "work", "deliveries", "list", "--endpoint", "ep-acc", "--source-id", "src-held")
	if err != nil {
		t.Fatalf("identity lookup failed: %v\n%s", err, lookupOut)
	}
	if !strings.Contains(lookupOut, "dlv-held-00001") || strings.Contains(lookupOut, "dlv-ignored-01") {
		t.Fatalf("the identity lookup returned the wrong rows:\n%s", lookupOut)
	}
	badOut, err := runWorkCLI(t, cfgPath, "work", "deliveries", "list", "--source-id", "src-held")
	if err == nil {
		t.Fatalf("expected --source-id without --endpoint to fail, got:\n%s", badOut)
	}

	getOut, err := runWorkCLI(t, cfgPath, "work", "deliveries", "get", "dlv-held-00001")
	if err != nil {
		t.Fatalf("work deliveries get failed: %v\n%s", err, getOut)
	}
	if !strings.Contains(getOut, "wk-replay-0001") || !strings.Contains(getOut, "held") {
		t.Fatalf("the delivery detail is missing its work id or payload state:\n%s", getOut)
	}
	if strings.Contains(getOut, workAcceptanceWebhookSecret) {
		t.Fatalf("the delivery detail printed the raw payload:\n%s", getOut)
	}

	expiredOut, err := runWorkCLI(t, cfgPath, "work", "deliveries", "get", "dlv-expired-01")
	if err != nil {
		t.Fatalf("work deliveries get (expired) failed: %v\n%s", err, expiredOut)
	}
	if !strings.Contains(expiredOut, "replay of this delivery is unavailable") {
		t.Fatalf("a dropped payload must say the replay is unavailable:\n%s", expiredOut)
	}
}

// The CLI documents a state and source vocabulary in docs/cli/work.mdx. This
// asserts the server accepts every value of it — a documented filter the API
// rejects is a doc that lies, and the values are typed in three places (the
// ledger's constants, the handler's map, the flag help).
//
// The list is read with -f json because the table shortens ids for width, and
// an assertion against a truncated id is an assertion about column width.
func TestAcceptance_WorkList_FilterVocabularyMatchesTheServer(t *testing.T) {
	cfgPath := startWorkAcceptanceServer(t)

	out, err := runWorkCLI(t, cfgPath, "work", "list", "--source", "webhook", "-f", "json")
	if err != nil {
		t.Fatalf("work list --source webhook failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "wk-replay-0001") {
		t.Fatalf("--source webhook did not find the seeded webhook work:\n%s", out)
	}
	if strings.Contains(out, "wk-queued-0001") {
		t.Fatalf("--source webhook returned manual work too:\n%s", out)
	}
	if strings.Contains(out, "input_json") {
		t.Fatalf("the JSON list carries the accepted input:\n%s", out)
	}

	for _, state := range []string{
		string(work.StateQueued), string(work.StateStarting), string(work.StateRunning),
		string(work.StateWaiting), string(work.StateRetryWait), string(work.StateSucceeded),
		string(work.StateFailed), string(work.StateExpired), string(work.StateCancelled),
		string(work.StateNeedsReconciliation),
	} {
		if out, err := runWorkCLI(t, cfgPath, "work", "list", "--state", state); err != nil {
			t.Fatalf("--state %s was refused by the server: %v\n%s", state, err, out)
		}
	}
	for _, source := range []string{
		string(work.SourceWebhook), string(work.SourceChat), string(work.SourceAssignment),
		string(work.SourceSchedule), string(work.SourcePipelineStep), string(work.SourceManual),
	} {
		if out, err := runWorkCLI(t, cfgPath, "work", "list", "--source", source); err != nil {
			t.Fatalf("--source %s was refused by the server: %v\n%s", source, err, out)
		}
	}
	for _, class := range []string{string(work.ClassChat), string(work.ClassBackground)} {
		if out, err := runWorkCLI(t, cfgPath, "work", "list", "--class", class); err != nil {
			t.Fatalf("--class %s was refused by the server: %v\n%s", class, err, out)
		}
	}
}
