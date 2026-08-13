package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// convSearchAuthedRequest drives the production Router's
// POST /api/v1/conversations/search as an authenticated member of wsID.
func convSearchAuthedRequest(t *testing.T, opts ...RouterOption) *httptest.ResponseRecorder {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-cs", wsID, "C", "c-convsearch")
	seedAgentRow(t, db, "agent-cs", wsID, "crew-cs", "A", "a-convsearch", "AGENT")

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger(), opts...)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(context.Background(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/search?workspace_id="+wsID,
		strings.NewReader(`{"query":"deploy"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestConversationSearchRoute_ReachableWhenWired is the regression that the
// endpoint has a caller at all: with WithConversationSearch supplied — as
// the server supplies it at boot — the route answers the query instead of
// 503 "not configured".
func TestConversationSearchRoute_ReachableWhenWired(t *testing.T) {
	rr := convSearchAuthedRequest(t, WithConversationSearch(&stubMultiConversationSearcher{}))
	if rr.Code == http.StatusServiceUnavailable {
		t.Fatalf("route still answers 503 with the searcher wired: %s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}

// TestConversationSearchRoute_503WithoutSearcher pins the other half of the
// contract: unconfigured is 503, not a panic and not an empty 200 that would
// read as "no history".
func TestConversationSearchRoute_503WithoutSearcher(t *testing.T) {
	rr := convSearchAuthedRequest(t)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
}
