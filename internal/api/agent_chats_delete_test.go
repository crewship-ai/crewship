package api

// Tests for DELETE /api/v1/agents/{agentId}/chats/{chatId} (#998).
//
// The endpoint exists so one-shot programmatic chats (routine iterate's
// grader/optimizer calls) can clean up after themselves instead of
// stranding orphan sessions in the sidebar — and gives operators the
// same ability for any chat they created.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func seedChatForDelete(t *testing.T, h *AgentHandler, wsID, agentID, chatID, createdBy string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other')
		 ON CONFLICT(id) DO NOTHING`, createdBy, createdBy+"@example.com"); err != nil {
		t.Fatalf("seed creator user: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES (?, ?, 'A', ?, 'IDLE')
		 ON CONFLICT(id) DO NOTHING`, agentID, wsID, agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, status) VALUES (?, ?, ?, ?, 'ACTIVE')`,
		chatID, agentID, wsID, createdBy); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO conversation_messages (id, session_id, agent_id, role, content, ts)
		 VALUES (?, ?, ?, 'user', 'hi', '2026-01-01T00:00:00Z')`, chatID+"-m1", chatID, agentID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func deleteChatReq(t *testing.T, h *AgentHandler, userID, wsID, role, agentID, chatID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/chats/"+chatID, nil)
	r.SetPathValue("agentId", agentID)
	r.SetPathValue("chatId", chatID)
	r = withWorkspaceUser(r, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.DeleteChat(rr, r)
	return rr
}

func TestDeleteChat_CreatorDeletesOwnChat(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-dc", "chat-dc-1", userID)

	rr := deleteChatReq(t, h, userID, wsID, "MEMBER", "agent-dc", "chat-dc-1")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("creator delete: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE id = 'chat-dc-1'`).Scan(&n)
	if n != 0 {
		t.Error("chat row must be gone")
	}
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM conversation_messages WHERE session_id = 'chat-dc-1'`).Scan(&n)
	if n != 0 {
		t.Error("chat messages must be gone with the chat")
	}
}

func TestDeleteChat_NonCreatorMemberForbidden(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-dc2", "chat-dc-2", "someone-else")

	rr := deleteChatReq(t, h, userID, wsID, "MEMBER", "agent-dc2", "chat-dc-2")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-creator MEMBER delete: status = %d, want 403", rr.Code)
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE id = 'chat-dc-2'`).Scan(&n)
	if n != 1 {
		t.Error("chat must survive a forbidden delete")
	}
}

func TestDeleteChat_AdminDeletesAnyChat(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-dc3", "chat-dc-3", "someone-else")

	rr := deleteChatReq(t, h, userID, wsID, "ADMIN", "agent-dc3", "chat-dc-3")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("admin delete: status = %d, body: %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteChat_WrongAgentOrWorkspace404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-dc4", "chat-dc-4", userID)

	// Chat exists but belongs to a different agent → 404.
	rr := deleteChatReq(t, h, userID, wsID, "OWNER", "agent-other", "chat-dc-4")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("wrong-agent delete: status = %d, want 404", rr.Code)
	}

	// Unknown chat → 404.
	rr = deleteChatReq(t, h, userID, wsID, "OWNER", "agent-dc4", "ghost-chat")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown chat delete: status = %d, want 404", rr.Code)
	}
}
