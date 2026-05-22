package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// TestChatMessagesRoute_AppliesWsCtx pins issue #539. The
// GET /api/v1/chats/{chatId}/messages route's handler runs
// canRole(RoleFromContext(...), "read"), which fail-closes 403 whenever
// no middleware has populated the role context value. The route is
// supposed to be wrapped in authed(wsCtx(...)) so wsCtx
// (RequireWorkspace) puts the user's role on the request — without that
// wrapper every authenticated user including OWNER hits 403.
//
// The test sends a valid bearer to the route through the production
// Router without a ?workspace_id= query. With wsCtx wired the request
// short-circuits at 400 ("workspace_id is required") before reaching
// the handler. With wsCtx missing the handler runs, sees role="" and
// returns 403 — that's the regression we're guarding against.
func TestChatMessagesRoute_AppliesWsCtx(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	const secret = "test-secret-for-jwt-signing-32chars!!"

	r, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-chatmsgs-route-test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("auth.NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/chats/any-chat-id/messages", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	switch rr.Code {
	case http.StatusBadRequest:
		// wsCtx fired first — issue #539 fix is in place.
	case http.StatusForbidden:
		t.Errorf("regression of issue #539: route returned 403, meaning wsCtx is missing from the chain and the handler's role gate fail-closed on an empty role; body: %s", rr.Body.String())
	default:
		t.Errorf("unexpected status = %d (want 400 from wsCtx); body: %s", rr.Code, rr.Body.String())
	}
}
