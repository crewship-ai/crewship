package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestKeeperDecisionDurablyCorrelatesCallingRun(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedKeeperFixture(t, db)
	writer := newKeeperTestJournal(t, db)
	const run = "keeper-fixture-run"
	if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture run started"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "fixture", RiskScore: 1}}
	h := NewKeeperHandler(db, "synthetic-internal", gk, keeperEventsLogger())
	h.journal = writer
	body, _ := json.Marshal(map[string]string{"workspace_id": ws, "requesting_crew_id": crew, "requesting_agent_id": agent, "requesting_run_id": run, "credential_id": credential, "intent": "fixture access"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.HandleRequest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("request returned %d: %s", w.Code, w.Body.String())
	}
	id := latestRequestID(t, db)
	var recorded string
	if err := db.QueryRow(`SELECT COALESCE(requesting_run_id,'') FROM keeper_requests WHERE id=?`, id).Scan(&recorded); err != nil || recorded != run {
		t.Fatalf("request lost calling run: %q, %v", recorded, err)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_request_events WHERE request_id=? AND requesting_run_id=?`, id, run).Scan(&events); err != nil || events != 2 {
		t.Fatalf("run-correlated pending/decision events=%d, %v", events, err)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type=? AND agent_id=? AND trace_id=?`, string(journal.EntryKeeperDecision), agent, run).Scan(&events); err != nil || events != 1 {
		t.Fatalf("journal lost decision correlation: %d, %v", events, err)
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata_json FROM credential_audit WHERE credential_id=? AND agent_id=? AND event_type='KEEPER_DECISION'`, credential, agent).Scan(&metadata); err != nil {
		t.Fatalf("Vault Activity has no Keeper decision: %v", err)
	}
	var audit map[string]any
	if err := json.Unmarshal([]byte(metadata), &audit); err != nil {
		t.Fatal(err)
	}
	logs := NewKeeperLogHandler(db, keeperEventsLogger())
	for _, surface := range []string{"list", "events"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("requestId", id)
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "test-user-id"}), ws, "ADMIN"))
		out := httptest.NewRecorder()
		if surface == "list" {
			logs.List(out, req)
		} else {
			logs.ListEvents(out, req)
		}
		var rows []map[string]any
		if err := json.Unmarshal(out.Body.Bytes(), &rows); err != nil || len(rows) == 0 || rows[0]["requesting_run_id"] != run {
			t.Errorf("%s API lost requesting_run_id: %d %s (%v)", surface, out.Code, out.Body.String(), err)
		}
	}
	var lastUsed string
	if err := db.QueryRow(`SELECT COALESCE(last_used_at,'') FROM credentials WHERE id=?`, credential).Scan(&lastUsed); err != nil || lastUsed != "" {
		t.Errorf("Keeper decision incorrectly recorded USE: %q %v", lastUsed, err)
	}
	if _, err := db.Exec(`UPDATE keeper_requests SET requesting_run_id='another-run' WHERE id=?`, id); err == nil {
		t.Error("requesting run attribution could be rewritten")
	}
	if audit["run_id"] != run || audit["keeper_request_id"] != id || audit["decision"] != "ALLOW" {
		t.Fatalf("Vault Activity lost decision identity: %v", audit)
	}
}

func TestKeeperInvalidRunDoesNotConsumeHumanApproval(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedL4Fixture(t, db)
	h, _ := newL4Handler(t, db, nil, nil)
	initial := doKeeperRequest(h, keeperRequestBody{RequestingAgentID: agent, RequestingCrewID: crew, WorkspaceID: ws, CredentialID: credential, Intent: "fixture access in approved change window"})
	if initial.Code != http.StatusOK {
		t.Fatalf("initial escalation: %d %s", initial.Code, initial.Body.String())
	}
	id := latestRequestID(t, db)
	var owner string
	if err := db.QueryRow(`SELECT created_by FROM credentials WHERE id=?`, credential).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if approved := resolveDecision(t, h, ws, id, "ALLOW", owner); approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	denied := doKeeperRequest(h, keeperRequestBody{RequestingAgentID: agent, RequestingRunID: "missing-run", RequestingCrewID: crew, WorkspaceID: ws, CredentialID: credential, Intent: "fixture access in approved change window", ApprovalRequestID: id})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("invalid calling run returned %d: %s", denied.Code, denied.Body.String())
	}
	var consumed bool
	if err := db.QueryRow(`SELECT approval_consumed_at IS NOT NULL FROM keeper_requests WHERE id=?`, id).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed {
		t.Fatal("invalid calling run consumed the human approval before run admission")
	}
}

func TestKeeperHumanDecisionKeepsOriginalRunAndType(t *testing.T) {
	for _, kind := range []string{"access", "execute"} {
		t.Run(kind, func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, agent, credential := seedKeeperFixture(t, db)
			writer := newKeeperTestJournal(t, db)
			const run = "keeper-original-run"
			if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture run started"}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			h := NewKeeperHandler(db, "synthetic-internal", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionEscalate), Reason: "fixture approval needed", RiskScore: 8}}, keeperEventsLogger()).WithSecrets(&mockSecretGetter{secrets: map[string]string{credential: "synthetic-fixture-value"}}).WithContainer(&mockContainerExec{output: "fixture", exitCode: 0, execID: "fixture-exec"})
			h.SetJournal(writer)
			var result *httptest.ResponseRecorder
			if kind == "access" {
				result = doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture approval"})
			} else {
				result = doKeeperExecute(h, keeperExecuteBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture approval", Command: "true", ContainerID: "test-container"})
			}
			if result.Code != http.StatusOK {
				t.Fatalf("escalation: %d %s", result.Code, result.Body.String())
			}
			id := latestRequestID(t, db)
			if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryRunCompleted, ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture run ended"}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			r := resolveReq(t, ws, "ADMIN", "fixture-approver", id, map[string]any{"decision": "ALLOW", "reason": "fixture approved"})
			r = r.WithContext(journal.WithRunID(r.Context(), "unrelated-human-http-run"))
			w := httptest.NewRecorder()
			h.HandleResolve(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("human decision: %d %s", w.Code, w.Body.String())
			}
			var savedRun, savedType string
			if err := db.QueryRow(`SELECT COALESCE(requesting_run_id,''),request_type FROM keeper_request_events WHERE request_id=? ORDER BY seq DESC LIMIT 1`, id).Scan(&savedRun, &savedType); err != nil || savedRun != run || savedType != kind {
				t.Fatalf("human ledger attribution %q/%q: %v", savedRun, savedType, err)
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type=? AND agent_id=? AND trace_id=? AND json_extract(refs,'$.keeper_request_id')=? AND json_extract(payload,'$.decision')='ALLOW'`, string(journal.EntryKeeperDecision), agent, run, id).Scan(&n); err != nil || n != 1 {
				t.Fatalf("human journal lost original run/request/credential attribution: %d %v", n, err)
			}
		})
	}
}
