package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// The same-host workbook editor learns which public origin browsers use from
// requests a sign-in session authenticated, never from unauthenticated
// requests or CLI tokens (WithWorkbookEditor). The observer runs after
// authentication and does not change the request's outcome.
func TestRequireAuthObservesOnlySessionRequests(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.NewDBStore(db)
	sess, err := store.Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := v.IssueAccessToken(userID, sess.ID, "Test", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	cliToken := "crewship_cli_observer00112233445566778899"
	if _, err := db.Exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-obs', ?, 'cli', ?, datetime('now'))`, userID, sha256Hex(cliToken)); err != nil {
		t.Fatal(err)
	}
	mw := NewAuthMiddleware(v, store, db, logger)
	var seen []string
	mw.observeSession = func(r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			t.Error("observer ran before authentication")
		}
		seen = append(seen, r.Header.Get("X-Case"))
	}
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, authz string
		cookie      bool
		want        int
	}{
		{"session bearer", "Bearer " + jwt, false, 204},
		{"session cookie", "", true, 204},
		{"cli token", "Bearer " + cliToken, false, 204},
		{"bad token", "Bearer nope.nope.nope", false, 401},
		{"anonymous", "", false, 401},
	} {
		r := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
		r.Header.Set("X-Case", tc.name)
		if tc.authz != "" {
			r.Header.Set("Authorization", tc.authz)
		}
		if tc.cookie {
			r.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: jwt})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: status %d, want %d", tc.name, w.Code, tc.want)
		}
	}
	if len(seen) != 2 || seen[0] != "session bearer" || seen[1] != "session cookie" {
		t.Fatalf("observed %v, want only the two session requests", seen)
	}
}
