package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// AgentChatAttachment (POST /api/v1/agents/{agentId}/chats/{chatId}/attachments)
// gates on the "create" role, requires both path ids, and 404s an agent
// that isn't in the caller's workspace — all before any multipart parse.
// The file-write happy path needs container storage and is covered by
// acceptance tests.

func TestAgentChatAttachment_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewProxyHandler(db, newTestLogger(), "")
	req := httptest.NewRequest("POST", "/api/v1/agents/a1/chats/c1/attachments", nil)
	req.SetPathValue("agentId", "a1")
	req.SetPathValue("chatId", "c1")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAgentChatAttachment_MissingIDs(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewProxyHandler(db, newTestLogger(), "")
	req := httptest.NewRequest("POST", "/api/v1/agents//chats//attachments", nil)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAgentChatAttachment_AgentNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewProxyHandler(db, newTestLogger(), "")
	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/chats/c1/attachments", nil)
	req.SetPathValue("agentId", "ghost")
	req.SetPathValue("chatId", "c1")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}
