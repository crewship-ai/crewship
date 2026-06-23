package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// escalation_handler_cov_test.go — remaining branches: the bound-token
// crew/chat workspace guards on CreateEscalation, the CREDENTIAL
// encrypt failure, the resolve UPDATE failure, and the lost-race 409
// (RAISE(IGNORE) makes the UPDATE touch zero rows after the PENDING
// read). Helpers prefixed covEsc.

func covEscFixture(t *testing.T) (*QueryHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covesc-crew", wsID, "Crew", "covesc-crew")
	agentID := seedAgentRow(t, db, "covesc-ag", wsID, crewID, "Agent", "covesc-ag", "AGENT")
	h := NewQueryHandler(db, nil, nil, "", newTestLogger())
	return h, userID, wsID, crewID, agentID
}

func covEscSeed(t *testing.T, h *QueryHandler, id, wsID, crewID, agentID, escType string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
		VALUES (?, ?, ?, 'covesc-chat', ?, 'need help', ?, 'PENDING', datetime('now'))`,
		id, wsID, crewID, agentID, escType)
}

// TestCovEsc_Create_BoundTokenCrewMismatch_403 — a ws-bound internal
// token cannot raise an escalation attributed to a crew outside its
// workspace.
func TestCovEsc_Create_BoundTokenCrewMismatch_403(t *testing.T) {
	h, _, wsID, _, _ := covEscFixture(t)
	// workspace_id matches the bound token, but crew_id can't be proven
	// to live in that workspace (unknown crew) → guard refuses.
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(map[string]string{
		"from_slug": "covesc-ag", "reason": "r", "crew_id": "ghost-crew",
		"workspace_id": wsID, "chat_id": "covesc-chat",
	}))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovEsc_Create_BoundTokenChatMismatch_403 — same closure for
// chat_id: an unknown chat fails the bound-workspace proof.
func TestCovEsc_Create_BoundTokenChatMismatch_403(t *testing.T) {
	h, _, wsID, crewID, _ := covEscFixture(t)
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(map[string]string{
		"from_slug": "covesc-ag", "reason": "r", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "ghost-chat",
	}))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func covEscResolve(h *QueryHandler, userID, wsID, escID string, body map[string]string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/escalations/"+escID+"/resolve", jsonBody(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("escalationId", escID)
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	return rr
}

func TestCovEsc_Resolve_CredentialEncryptFailure_500(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "definitely-not-hex")
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	covEscSeed(t, h, "covesc-e1", wsID, crewID, agentID, "CREDENTIAL")
	rr := covEscResolve(h, userID, wsID, "covesc-e1", map[string]string{
		"resolution": "hunter2", "action": "approve",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovEsc_Resolve_UpdateError_500(t *testing.T) {
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	covEscSeed(t, h, "covesc-e2", wsID, crewID, agentID, "TEXT")
	execOrFatal(t, h.db, `CREATE TRIGGER covesc_block_upd BEFORE UPDATE ON escalations
		BEGIN SELECT RAISE(ABORT, 'covesc forced'); END`)
	rr := covEscResolve(h, userID, wsID, "covesc-e2", map[string]string{
		"resolution": "done", "action": "approve",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovEsc_Resolve_LostRace_409 — the UPDATE matches zero rows
// (simulated via RAISE(IGNORE)) after the PENDING read: conflict.
func TestCovEsc_Resolve_LostRace_409(t *testing.T) {
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	covEscSeed(t, h, "covesc-e3", wsID, crewID, agentID, "TEXT")
	execOrFatal(t, h.db, `CREATE TRIGGER covesc_ignore_upd BEFORE UPDATE ON escalations
		BEGIN SELECT RAISE(IGNORE); END`)
	rr := covEscResolve(h, userID, wsID, "covesc-e3", map[string]string{
		"resolution": "done", "action": "approve",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}
