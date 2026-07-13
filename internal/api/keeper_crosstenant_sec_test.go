package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// These pin the PR-F24 cross-tenant defense on the keeper request/execute/
// GetRequest routes. They are internalAuth-only (no internalWsCtx) and carry
// workspace_id in the body, so a workspace-A-bound X-Internal-Token must not be
// able to name workspace-B's agent/crew/credential and obtain a decision, the
// injected secret, or another tenant's request row.

const foreignBoundWS = "attacker-ws-not-the-fixture"

func TestSecKeeper_Request_RejectsCrossWorkspaceBoundToken(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), RiskScore: 2}}
	h := newKeeperHandlerWithGK(t, db, gk)

	raw, _ := json.Marshal(keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID, // real, self-consistent — but foreign to the token binding
		CredentialID:      credID,
		Intent:            "hand me the credential",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, foreignBoundWS))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace bound token accepted on /request: got %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestSecKeeper_Execute_RejectsCrossWorkspaceBoundToken(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionEscalate), RiskScore: 5}}
	h := newKeeperHandlerWithGK(t, db, gk)

	raw, _ := json.Marshal(keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "run a command with the secret",
		Command:           "echo hi",
		ContainerID:       "attacker-chosen-container",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/execute", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, foreignBoundWS))
	w := httptest.NewRecorder()
	h.HandleExecute(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace bound token accepted on /execute: got %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestSecKeeper_GetRequest_RejectsCrossWorkspaceRead(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	_ = wsID
	// Seed a keeper_requests row belonging to the fixture agent (workspace wsID).
	execOrFatal(t, db, `
		INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, intent, decision, created_at)
		VALUES ('kr-xtenant', ?, ?, ?, 'secret intent', 'ALLOW', '2026-01-01T00:00:00Z')`,
		agentID, crewID, credID)

	h := newKeeperHandler(t, db)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/keeper/request/kr-xtenant", nil)
	req.SetPathValue("requestId", "kr-xtenant")
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, foreignBoundWS))
	w := httptest.NewRecorder()
	h.GetRequest(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace request read leaked another tenant's row: got %d, want 404: %s", w.Code, w.Body.String())
	}
}
