package api

import (
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
