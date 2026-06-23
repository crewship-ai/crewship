package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// keeper_request_cov_test.go covers the remaining HandleRequest
// branches: input validation, credential_name resolution, DB-error
// 500s, the evaluate-error deny fallback, risk clamping, non-fatal
// insert/update/journal failures, the ESCALATE→inbox projection, and
// the broadcaster notification. Helpers are prefixed covKReq.

// covKReqErrEvaluator always fails evaluation, driving the
// deny-by-default fallback.
type covKReqErrEvaluator struct{}

func (covKReqErrEvaluator) Evaluate(_ context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	return keeper.GatekeeperResponse{}, errors.New("ollama on fire")
}

// covKReqFailEmitter is a journal.Emitter whose Emit always errors —
// keeper decisions must survive journal outages.
type covKReqFailEmitter struct{}

func (covKReqFailEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", errors.New("journal down")
}

func (covKReqFailEmitter) Flush(_ context.Context) error { return nil }

// covKReqBroadcaster records keeper events.
type covKReqBroadcaster struct {
	events []map[string]any
}

func (b *covKReqBroadcaster) BroadcastKeeperEvent(_ string, event map[string]any) {
	b.events = append(b.events, event)
}

func covKReqDecode(t *testing.T, raw []byte) keeper.RequestResult {
	t.Helper()
	var res keeper.RequestResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode result: %v (%s)", err, raw)
	}
	return res
}

func TestCovKReq_InvalidJSON_400(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandler(t, db)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request",
		strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.HandleRequest(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovKReq_MissingCredentialIDAndName_400(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandler(t, db)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "a", RequestingCrewID: "c", WorkspaceID: "w", Intent: "deploy something",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "credential_id or credential_name required") {
		t.Errorf("body = %s, want credential_id/name required error", rr.Body.String())
	}
}

func TestCovKReq_NameResolution_SuccessAllow(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialName: "PROD_SSH", Intent: "need ssh for deploy of release 1.2",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())
	if res.Decision != keeper.DecisionAllow || res.RiskScore != 2 {
		t.Errorf("result = %+v, want ALLOW risk 2", res)
	}
	// The persisted request must carry the resolved credential id.
	var gotCred string
	if err := db.QueryRow(`SELECT credential_id FROM keeper_requests WHERE id = ?`, res.RequestID).Scan(&gotCred); err != nil {
		t.Fatalf("read keeper_requests: %v", err)
	}
	if gotCred != credID {
		t.Errorf("credential_id = %q, want %q (resolved from name)", gotCred, credID)
	}
}

func TestCovKReq_NameResolution_NotFound_404(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialName: "NO_SUCH_ENV_VAR", Intent: "some legitimate looking intent",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "credential not found for name") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovKReq_AgentLookupDBError_500(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandler(t, db)
	db.Close()
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "a", RequestingCrewID: "c", WorkspaceID: "w",
		CredentialID: "cred", Intent: "some legitimate looking intent",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovKReq_CredentialLookupDBError_500(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	// Break ONLY the credential lookup: agents/crews stay intact so the
	// agent validation passes first.
	execOrFatal(t, db, `ALTER TABLE credentials RENAME TO credentials_broken`)
	h := newKeeperHandler(t, db)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "some legitimate looking intent",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovKReq_EvaluateError_DenyFallback(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandlerWithGK(t, db, covKReqErrEvaluator{})
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "deploy hotfix to production",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())
	if res.Decision != keeper.DecisionDeny || res.RiskScore != 10 {
		t.Errorf("result = %+v, want DENY risk 10 (deny-by-default)", res)
	}
	if !strings.Contains(res.Reason, "deny by default") {
		t.Errorf("reason = %q, want deny-by-default reason", res.Reason)
	}
}

func TestCovKReq_RiskScoreClampedToMinimum(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 0, // below valid range
	}}
	h := newKeeperHandlerWithGK(t, db, gk)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "read-only metrics scrape",
	})
	res := covKReqDecode(t, rr.Body.Bytes())
	if res.RiskScore != 1 {
		t.Errorf("risk = %d, want clamped to 1", res.RiskScore)
	}
}

// TestCovKReq_InsertAndUpdateFailures_NonFatal forces both keeper_requests
// writes to fail via RAISE(ABORT) triggers; the decision flow must still
// answer 200 with the gatekeeper verdict (writes are best-effort).
func TestCovKReq_InsertAndUpdateFailures_NonFatal(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	execOrFatal(t, db, `CREATE TRIGGER covkreq_block_ins BEFORE INSERT ON keeper_requests
		BEGIN SELECT RAISE(ABORT, 'covkreq forced insert failure'); END`)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 3,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the deploy key",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if res := covKReqDecode(t, rr.Body.Bytes()); res.Decision != keeper.DecisionAllow {
		t.Errorf("decision = %v, want ALLOW despite failed persistence", res.Decision)
	}
}

// TestCovKReq_UpdateDecisionFailure_NonFatal lets the INSERT succeed but
// fails the decision UPDATE.
func TestCovKReq_UpdateDecisionFailure_NonFatal(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	execOrFatal(t, db, `CREATE TRIGGER covkreq_block_upd BEFORE UPDATE ON keeper_requests
		BEGIN SELECT RAISE(ABORT, 'covkreq forced update failure'); END`)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 3,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the deploy key",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	// Row stays PENDING because the UPDATE was rejected.
	var decision string
	res := covKReqDecode(t, rr.Body.Bytes())
	if err := db.QueryRow(`SELECT decision FROM keeper_requests WHERE id = ?`, res.RequestID).Scan(&decision); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if decision != "PENDING" {
		t.Errorf("decision column = %q, want PENDING (update blocked)", decision)
	}
}

// TestCovKReq_JournalFailures_NonFatal — both journal emits error; the
// request still completes.
func TestCovKReq_JournalFailures_NonFatal(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)
	h.SetJournal(covKReqFailEmitter{})
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "pull metrics for weekly report",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovKReq_Escalate_ProjectsToInboxAndBroadcasts — ESCALATE writes a
// blocking inbox item and notifies the broadcaster.
func TestCovKReq_Escalate_ProjectsToInboxAndBroadcasts(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionEscalate), Reason: "needs human", RiskScore: 7,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "drop the production database",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())
	if res.Decision != keeper.DecisionEscalate {
		t.Fatalf("decision = %v, want ESCALATE", res.Decision)
	}

	var kind, priority string
	var blocking int
	if err := db.QueryRow(`SELECT kind, priority, blocking FROM inbox_items
		WHERE workspace_id = ? AND source_id = ?`, wsID, res.RequestID).
		Scan(&kind, &priority, &blocking); err != nil {
		t.Fatalf("inbox item not projected: %v", err)
	}
	if priority != "high" || blocking != 1 {
		t.Errorf("inbox item = kind %q priority %q blocking %d, want high/blocking", kind, priority, blocking)
	}

	if len(bc.events) != 1 {
		t.Fatalf("broadcast events = %d, want 1", len(bc.events))
	}
	if bc.events[0]["decision"] != string(keeper.DecisionEscalate) || bc.events[0]["request_id"] != res.RequestID {
		t.Errorf("broadcast = %v, want ESCALATE for %s", bc.events[0], res.RequestID)
	}
}
