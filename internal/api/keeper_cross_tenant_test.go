package api

// keeper_cross_tenant_test.go — regression for the cross-tenant confused
// deputy on the internal keeper endpoints. A workspace-bound sidecar token
// must not be able to name a different workspace's workspace_id in the JSON
// body: the credential-access request/execute handlers read workspace_id
// from the body, so the body claim must be proven against the token binding
// (assertInternalTokenWorkspace), exactly like every sibling internal route.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// doKeeperRequestBound posts a request with the internal-token workspace
// binding set in context (what the auth middleware attaches for a
// workspace-scoped sidecar token).
func doKeeperRequestBound(h *KeeperHandler, body keeperRequestBody, boundWS string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, boundWS))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)
	return w
}

func doKeeperExecuteBound(h *KeeperHandler, body keeperExecuteBody, boundWS string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/execute", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, boundWS))
	w := httptest.NewRecorder()
	h.HandleExecute(w, req)
	return w
}

// A token bound to another workspace naming this workspace's real
// agent+credential is refused with 403 before any evaluation happens.
func TestKeeperRequest_CrossWorkspaceToken_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	// A gatekeeper that would ALLOW — proving the 403 is the binding gate,
	// not an evaluation outcome.
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 1,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	rr := doKeeperRequestBound(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "legitimate looking intent for deploy",
	}, "attacker-workspace")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "does not match the workspace bound") {
		t.Errorf("body = %s", rr.Body.String())
	}
	// No keeper_requests row should have been persisted — the gate fires
	// before the PENDING insert.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_requests WHERE requesting_agent_id = ?`, agentID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("cross-tenant request persisted %d rows, want 0", n)
	}
}

// The matching token (bound == body) passes the gate (reaches evaluation).
func TestKeeperRequest_MatchingToken_PassesGate(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 1,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	rr := doKeeperRequestBound(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "legitimate looking intent for deploy",
	}, wsID)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// A master-token caller (no binding in context) is unaffected — the guard
// is a no-op, preserving host-side trusted callers.
func TestKeeperRequest_MasterToken_Unaffected(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 1,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	// boundWS "" == no workspace-bound token in context.
	rr := doKeeperRequestBound(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "legitimate looking intent for deploy",
	}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// Execute is the high-value target: a cross-workspace token must be refused
// before the secret store is ever consulted.
func TestKeeperExecute_CrossWorkspaceToken_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 1,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	rr := doKeeperExecuteBound(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, ContainerID: "attacker-container",
		Intent: "run the nightly job", Command: "echo hi",
	}, "attacker-workspace")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "does not match the workspace bound") {
		t.Errorf("body = %s", rr.Body.String())
	}
}
