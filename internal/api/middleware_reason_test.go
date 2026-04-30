package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// The 401 reason code is the contract between server and frontend
// apiFetch — different reasons trigger different client behavior:
//   - session_expired   → try /api/auth/token/refresh once
//   - session_revoked   → terminal, hard-redirect
//   - session_invalid   → terminal, hard-redirect
//   - no_credentials    → no cookie at all (client may not be logged in)
//
// Each branch has its own test below.

func newMwRig(t *testing.T) (*AuthMiddleware, sessions.Store, *auth.JWTValidator, string, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	uid := seedTestUser(t, db)
	sess, err := store.Create(context.Background(), uid, "ua", "ip", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mw := NewAuthMiddleware(v, store, db, logger)
	return mw, store, v, uid, sess.ID
}

func extractErrorBody(rr *httptest.ResponseRecorder) string {
	t := struct {
		Error string `json:"error"`
	}{}
	_ = json.Unmarshal(rr.Body.Bytes(), &t)
	return t.Error
}

func TestMiddleware_NoCredentials(t *testing.T) {
	mw, _, _, _, _ := newMwRig(t)
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("inner handler should not run without auth")
	}))

	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if got := extractErrorBody(rr); got != "no_credentials" {
		t.Errorf("body error = %q, want no_credentials", got)
	}
	if !strings.Contains(rr.Header().Get("WWW-Authenticate"), "no_credentials") {
		t.Errorf("WWW-Authenticate missing reason: %q", rr.Header().Get("WWW-Authenticate"))
	}
}

func TestMiddleware_SessionRevoked(t *testing.T) {
	mw, store, v, uid, sid := newMwRig(t)
	tok, err := v.IssueAccessToken(uid, sid, "", "")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	// Revoke before the request hits.
	if err := store.Revoke(context.Background(), sid, sessions.ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("inner handler should not run on revoked session")
	}))
	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if got := extractErrorBody(rr); got != "session_revoked" {
		t.Errorf("error = %q, want session_revoked", got)
	}
}

func TestMiddleware_SessionRowMissing(t *testing.T) {
	// Session row deleted out of band (e.g. user deleted with FK
	// CASCADE, or DB cleanup job pruned an old row). The token still
	// verifies cryptographically, but Get returns ErrNotFound and we
	// map that to session_revoked — clients treat both terminally.
	//
	// Previous version of this test only Revoke'd, which left the row
	// present and exercised the same inactive-session branch already
	// covered by TestMiddleware_SessionRevoked. Now we DELETE the
	// row outright via FK CASCADE so this test actually hits the
	// ErrNotFound path.
	mw, _, v, uid, sid := newMwRig(t)
	tok, err := v.IssueAccessToken(uid, sid, "", "")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	// Delete the user → FK CASCADE deletes the user_sessions row.
	if _, err := mw.db.Exec(`DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("inner handler should not run when session row is gone")
	}))
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := extractErrorBody(rr); got != "session_revoked" {
		t.Errorf("error = %q, want session_revoked", got)
	}
}

func TestMiddleware_TokenExpired(t *testing.T) {
	// Verify the middleware maps ErrTokenExpired → session_expired.
	// Frontend's apiFetch wrapper branches on this exact reason code:
	// session_expired triggers a refresh attempt, anything else does
	// not. If this test ever stops working the user-visible behavior
	// "stale token silently rotates" is broken.
	mw, _, v, uid, sid := newMwRig(t)
	// Inject a clock 1 hour in the past on the validator. The issued
	// token will have iat/exp anchored to that past moment, so by the
	// time the middleware's real-time clock validates it, the token
	// is well past expiry.
	v.SetClock(func() time.Time { return time.Now().Add(-2 * time.Hour) })
	tok, err := v.IssueAccessToken(uid, sid, "", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	v.SetClock(time.Now) // restore for the middleware's validate call

	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("expired token must not reach inner handler")
	}))
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := extractErrorBody(rr); got != "session_expired" {
		t.Errorf("error = %q, want session_expired", got)
	}
}

func TestMiddleware_GarbageBearerToken(t *testing.T) {
	mw, _, _, _, _ := newMwRig(t)
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer garbage.not.a.jwe")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := extractErrorBody(rr); got != "session_invalid" {
		t.Errorf("error = %q, want session_invalid", got)
	}
}

func TestMiddleware_RefreshTokenSmuggledIntoBearerHeader(t *testing.T) {
	// kind=refresh tokens must NOT be honored as access tokens. The
	// validator's per-kind salt makes this fail at decrypt → maps to
	// session_invalid.
	mw, _, v, uid, sid := newMwRig(t)
	refresh, err := v.IssueRefreshToken(uid, sid)
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}

	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("refresh-as-access must be rejected")
	}))
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+refresh)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := extractErrorBody(rr); got != "session_invalid" {
		t.Errorf("error = %q, want session_invalid", got)
	}
}

func TestMiddleware_HappyPathPopulatesUserAndSessionID(t *testing.T) {
	mw, _, v, uid, sid := newMwRig(t)
	tok, err := v.IssueAccessToken(uid, sid, "Tester", "test@example.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	var seen *AuthUser
	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if seen == nil {
		t.Fatal("user not set in context")
	}
	if seen.ID != uid {
		t.Errorf("user id mismatch: %s vs %s", seen.ID, uid)
	}
	if seen.SessionID != sid {
		t.Errorf("session id mismatch: %s vs %s", seen.SessionID, sid)
	}
}

func TestMiddleware_SessionLastUsedTouched(t *testing.T) {
	mw, store, v, uid, sid := newMwRig(t)
	tok, err := v.IssueAccessToken(uid, sid, "", "")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	// Snapshot last_used_at from the store. Because Create just ran,
	// last_used_at == created_at to the second.
	pre, err := store.Get(context.Background(), sid)
	if err != nil {
		t.Fatalf("get pre: %v", err)
	}

	// Bump system clock past the throttle window in the store. We can't
	// move real time, so we cast to *DBStore and rotate its clock.
	dbs, ok := store.(*sessions.DBStore)
	if !ok {
		t.Skip("store is not DBStore — skipping clock manipulation test")
	}
	dbs.SetClock(func() time.Time { return time.Now().Add(2 * sessions.LastUsedThrottle) })

	handler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	post, gErr := store.Get(context.Background(), sid)
	if gErr != nil {
		t.Fatalf("get post-touch: %v", gErr)
	}
	if !post.LastUsedAt.After(pre.LastUsedAt) {
		t.Errorf("last_used_at not advanced; pre=%v post=%v", pre.LastUsedAt, post.LastUsedAt)
	}
}

// --- helpers ---------------------------------------------------------------

// (encryptForgedAccessToken removed — superseded by validator.SetClock,
// which lets tests issue tokens with arbitrary iat/exp without
// reaching into the internals.)
