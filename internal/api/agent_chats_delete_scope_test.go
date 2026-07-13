package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDeleteChatRouteIsScopeExempt locks the #1074 scope-symmetry fix. Chat
// create (POST) and read (PUT .../read) register roleSelf → scopeSelf
// (ownership-gated, scope-exempt), so a narrowly-scoped CLI token can spin up
// one-shot programmatic chats. The DELETE used to declare agents:write via
// scopeForRoute, so that SAME token got a 403 on every best-effort cleanup —
// noisy stderr per grader/optimizer call, and the orphan-chat problem it was
// meant to solve came back for scoped tokens. Since DeleteChat already runs its
// own creator-or-editor gate, the route must be scope-exempt (scopeSelf) too.
func TestDeleteChatRouteIsScopeExempt(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	const (
		createPat = "/api/v1/agents/{agentId}/chats"
		deletePat = "/api/v1/agents/{agentId}/chats/{chatId}"
	)
	scopeOf := func(method, pattern string) (string, bool) {
		for _, mr := range r.mutationRoutes {
			if mr.Method == method && mr.Pattern == pattern {
				return mr.Scope, true
			}
		}
		return "", false
	}

	createScope, ok := scopeOf("POST", createPat)
	if !ok {
		t.Fatalf("chat create route %q not recorded", createPat)
	}
	if createScope != scopeSelf {
		t.Fatalf("precondition: chat create scope = %q, want scopeSelf (symmetry baseline)", createScope)
	}

	deleteScope, ok := scopeOf("DELETE", deletePat)
	if !ok {
		t.Fatalf("chat delete route %q not recorded", deletePat)
	}
	if deleteScope != scopeSelf {
		t.Errorf("DELETE chat scope = %q, want scopeSelf — a scoped CLI token that can create/read one-shot chats must be able to clean them up too (#1074); DeleteChat's own creator-or-editor gate is the authorization, so the route is ownership-gated, not resource-capability gated", deleteScope)
	}
}

// deleteChatReqScoped mirrors deleteChatReq but attaches a CLI-token scope
// set to the request context (the shape AuthMiddleware stashes under
// ctxTokenScopes), so the handler-level scope gate can be exercised.
func deleteChatReqScoped(t *testing.T, h *AgentHandler, userID, wsID, role, agentID, chatID string, scopes ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/chats/"+chatID, nil)
	r.SetPathValue("agentId", agentID)
	r.SetPathValue("chatId", chatID)
	r = withWorkspaceUser(r, userID, wsID, role)
	set := make(stringSet, len(scopes))
	for _, s := range scopes {
		set[s] = struct{}{}
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxTokenScopes, set))
	rr := httptest.NewRecorder()
	h.DeleteChat(rr, r)
	return rr
}

// TestDeleteChat_ScopedTokenCreatorSelfCleanup pins the #1074 use case the
// scopeSelf registration exists for: a CLI token deliberately narrowed to an
// UNRELATED scope, held by the chat's creator, may still delete its own chat
// (ownership is the authorization; no resource capability is consumed).
func TestDeleteChat_ScopedTokenCreatorSelfCleanup(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-sc1", "chat-sc-1", userID)

	rr := deleteChatReqScoped(t, h, userID, wsID, "MEMBER", "agent-sc1", "chat-sc-1", "skills:write")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("creator self-cleanup with narrowed token: status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE id = 'chat-sc-1'`).Scan(&n)
	if n != 0 {
		t.Error("creator's own chat must be gone")
	}
}

// TestDeleteChat_ScopedTokenNonCreatorAdminForbidden closes the hole the
// roleSelf registration opened: the route-level scope gate no longer runs, so
// without a handler-level check a leaked CLI token narrowed to e.g.
// credentials:write held by an OWNER/ADMIN could delete ANY chat of ANY agent
// in the workspace via the canEditAgent arm — defeating scope narrowing for a
// destructive mutation. The non-creator arm must consume agents:write.
func TestDeleteChat_ScopedTokenNonCreatorAdminForbidden(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-sc2", "chat-sc-2", "someone-else")

	rr := deleteChatReqScoped(t, h, userID, wsID, "ADMIN", "agent-sc2", "chat-sc-2", "skills:write")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-creator ADMIN with narrowed token: status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE id = 'chat-sc-2'`).Scan(&n)
	if n != 1 {
		t.Error("chat must survive a scope-forbidden delete")
	}
}

// TestDeleteChat_ScopedTokenNonCreatorAdminWithAgentsWrite verifies the
// non-creator arm still works when the token actually carries agents:write —
// the scope gate consumes exactly the capability scopeForRoute used to demand
// at the route level, no more.
func TestDeleteChat_ScopedTokenNonCreatorAdminWithAgentsWrite(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-sc3", "chat-sc-3", "someone-else")

	rr := deleteChatReqScoped(t, h, userID, wsID, "ADMIN", "agent-sc3", "chat-sc-3", "agents:write")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("non-creator ADMIN with agents:write token: status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestDeleteChat_UnscopedAdminStillDeletesAny locks that the canEditAgent arm
// is untouched for JWT / unrestricted callers: an ADMIN with no token scope
// set deletes another creator's chat exactly as before.
func TestDeleteChat_UnscopedAdminStillDeletesAny(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-sc4", "chat-sc-4", "someone-else")

	rr := deleteChatReq(t, h, userID, wsID, "ADMIN", "agent-sc4", "chat-sc-4")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unscoped admin delete: status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
}
