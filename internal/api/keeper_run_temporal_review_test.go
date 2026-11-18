package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestKeeperEndedDuringJudgeCannotIssueLease(t *testing.T) {
	for _, kind := range []string{"access", "execute"} {
		t.Run(kind, func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, agent, credential := seedKeeperFixture(t, db)
			enableAutoLease(t, db, ws, 900)
			writer := newKeeperTestJournal(t, db)
			const run = "keeper-lease-run"
			if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture starts"}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctr := &mockContainerExec{output: "fixture", exitCode: 0, execID: "fixture-exec"}
			h := NewKeeperHandler(db, "synthetic-internal", &keeperEndRunDuringJudge{t: t, writer: writer, workspace: ws, crew: crew, agent: agent, run: run}, keeperEventsLogger()).WithSecrets(&mockSecretGetter{secrets: map[string]string{credential: "synthetic-fixture-value"}}).WithContainer(ctr)
			body, _ := json.Marshal(map[string]string{"workspace_id": ws, "requesting_crew_id": crew, "requesting_agent_id": agent, "requesting_run_id": run, "credential_id": credential, "intent": "fixture", "command": "true", "container_id": "test-container"})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/"+kind, strings.NewReader(string(body)))
			out := httptest.NewRecorder()
			if kind == "access" {
				h.HandleRequest(out, req)
			} else {
				h.HandleExecute(out, req)
			}
			var expiry, decision string
			if err := db.QueryRow(`SELECT COALESCE(expires_at,'') FROM agent_credentials WHERE agent_id=? AND credential_id=?`, agent, credential).Scan(&expiry); err != nil {
				t.Fatal(err)
			}
			if expiry != "" {
				t.Errorf("ended run mutated lease expiry: %q", expiry)
			}
			if err := db.QueryRow(`SELECT decision FROM keeper_requests ORDER BY created_at DESC LIMIT 1`).Scan(&decision); err != nil || decision != "DENY" {
				t.Errorf("ended run lacks durable refusal: %q %v", decision, err)
			}
			if ctr.lastExecContainerID != "" {
				t.Error("ended run executed")
			}
		})
	}
}

// A deterministic DB failpoint ends the run exactly when the approval UPDATE
// starts. With autocommit consumption the spend survives a later admission
// failure. A single consume/request/ledger transaction must roll it back.
func TestKeeperApprovalSpendAndRequestAdmissionAreAtomic(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedL4Fixture(t, db)
	h, _ := newL4Handler(t, db, nil, nil)
	initial := doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, CredentialID: credential, Intent: "fixture approved window"})
	if initial.Code != http.StatusOK {
		t.Fatalf("initial: %d", initial.Code)
	}
	approval := latestRequestID(t, db)
	var owner string
	if err := db.QueryRow(`SELECT created_by FROM credentials WHERE id=?`, credential).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if out := resolveDecision(t, h, ws, approval, "ALLOW", owner); out.Code != http.StatusOK {
		t.Fatalf("resolve: %d", out.Code)
	}
	const run = "keeper-spend-race"
	execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('spend-start',?,?,?,'run.started','orchestrator','fixture','{}',?)`, ws, crew, agent, run)
	// IDs are test-generated identifiers, not arbitrary request input. Quote the
	// SQL literals as data before constructing this test-only trigger.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	trigger := fmt.Sprintf(`CREATE TRIGGER fixture_end_at_approval_spend BEFORE UPDATE OF approval_consumed_at ON keeper_requests WHEN OLD.id=%s AND NEW.approval_consumed_at IS NOT NULL BEGIN INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('spend-terminal',%s,%s,%s,'run.completed','orchestrator','fixture end at spend','{}',%s); END`, quote(approval), quote(ws), quote(crew), quote(agent), quote(run))
	execOrFatal(t, db, trigger)
	out := doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture approved window", ApprovalRequestID: approval})
	if out.Code != http.StatusForbidden {
		t.Errorf("race did not refuse run: %d", out.Code)
	}
	var consumed bool
	if err := db.QueryRow(`SELECT approval_consumed_at IS NOT NULL FROM keeper_requests WHERE id=?`, approval).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed {
		t.Error("failed run admission committed a one-use approval spend")
	}
	var requests, orphanEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&requests); err != nil || requests != 1 {
		t.Errorf("failed admission retained request: %d %v", requests, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_request_events WHERE request_id<>?`, approval).Scan(&orphanEvents); err != nil || orphanEvents != 0 {
		t.Errorf("failed admission retained ledger: %d %v", orphanEvents, err)
	}
}

func TestKeeperLegacyUnknownCannotInheritUnrelatedRun(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedKeeperFixture(t, db)
	writer := newKeeperTestJournal(t, db)
	h := NewKeeperHandler(db, "synthetic-internal", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: "ESCALATE", Reason: "fixture", RiskScore: 8}}, keeperEventsLogger())
	h.SetJournal(writer)
	body, _ := json.Marshal(keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, CredentialID: credential, Intent: "legacy fixture"})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req = req.WithContext(journal.WithRunID(req.Context(), "unrelated-http-run"))
	out := httptest.NewRecorder()
	h.HandleRequest(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("request: %d", out.Code)
	}
	id := latestRequestID(t, db)
	human := resolveReq(t, ws, "ADMIN", "fixture-approver", id, map[string]any{"decision": "ALLOW", "reason": "fixture approval"})
	human = human.WithContext(journal.WithRunID(human.Context(), "unrelated-http-run"))
	out = httptest.NewRecorder()
	h.HandleResolve(out, human)
	if out.Code != http.StatusOK {
		t.Fatalf("resolve: %d", out.Code)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='unrelated-http-run' AND entry_type IN ('keeper.request','keeper.decision')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy request inherited unrelated run: %d %v", n, err)
	}
}

func TestKeeperAccessCannotAllowWithoutVaultDecisionAudit(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedKeeperFixture(t, db)
	enableAutoLease(t, db, ws, 900)
	execOrFatal(t, db, `CREATE TRIGGER fixture_deny_vault_audit BEFORE INSERT ON credential_audit WHEN NEW.event_type='KEEPER_DECISION' BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
	h := NewKeeperHandler(db, "synthetic-internal", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: "ALLOW", Reason: "fixture", RiskScore: 1}}, keeperEventsLogger())
	out := doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, CredentialID: credential, Intent: "fixture audit required"})
	if out.Code != http.StatusInternalServerError {
		t.Errorf("missing durable decision returned %d", out.Code)
	}
	var expiry, decision string
	if err := db.QueryRow(`SELECT COALESCE(expires_at,'') FROM agent_credentials WHERE agent_id=? AND credential_id=?`, agent, credential).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	if expiry != "" {
		t.Errorf("audit failure mutated grant: %q", expiry)
	}
	if err := db.QueryRow(`SELECT decision FROM keeper_requests ORDER BY created_at DESC LIMIT 1`).Scan(&decision); err != nil || decision != "PENDING" {
		t.Errorf("rollback projection=%q %v", decision, err)
	}
}
