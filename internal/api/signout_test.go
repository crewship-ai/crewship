package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func newSignOutRig(t *testing.T) (*NextAuthHandler, *auth.JWTValidator, sessions.Store, string, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	uid := seedTestUser(t, db)
	sess, _ := store.Create(context.Background(), uid, "ua", "ip", auth.RefreshTokenTTL)
	h := NewNextAuthHandler(db, logger, v, store)
	return h, v, store, uid, sess.ID
}

// signOut must revoke the row in user_sessions, not just clear cookies.
// Otherwise a logged-out cookie that someone replays still passes the
// JWT signature check; the only thing stopping it is the session table.
func TestSignOut_RevokesRowWithAccessCookie(t *testing.T) {
	h, v, store, uid, sid := newSignOutRig(t)
	access, _ := v.IssueAccessToken(uid, sid, "", "")

	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: access})
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	got, err := store.Get(context.Background(), sid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RevokedAt == nil {
		t.Error("session should be revoked after signOut with access cookie")
	}
	if got.RevokedReason != sessions.ReasonLogout {
		t.Errorf("reason: got %q, want %q", got.RevokedReason, sessions.ReasonLogout)
	}
}

// B.4 fix: signOut must also work when only the refresh cookie is
// present (e.g. tab idled past the 15-min access expiry). Without this
// the row would stay active in user_sessions forever and pollute the
// "Active sessions" UI.
func TestSignOut_RevokesRowWithRefreshCookieOnly(t *testing.T) {
	h, v, store, uid, sid := newSignOutRig(t)
	refresh, _ := v.IssueRefreshToken(uid, sid)

	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	// No access cookie — only refresh.
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	got, _ := store.Get(context.Background(), sid)
	if got.RevokedAt == nil {
		t.Error("signOut with refresh-only cookie failed to revoke session row")
	}
}

func TestSignOut_NoCookiesIsStillOK(t *testing.T) {
	h, _, _, _, _ := newSignOutRig(t)

	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (signOut is always idempotent)", rr.Code)
	}
}

func TestSignOut_ClearsBothCookies(t *testing.T) {
	h, v, _, uid, sid := newSignOutRig(t)
	access, _ := v.IssueAccessToken(uid, sid, "", "")
	refresh, _ := v.IssueRefreshToken(uid, sid)

	req := httptest.NewRequest("POST", "/api/auth/signout", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: access})
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})
	rr := httptest.NewRecorder()
	h.SignOut(rr, req)

	clearedAccess := false
	clearedRefresh := false
	for _, c := range rr.Result().Cookies() {
		if c.MaxAge == -1 {
			if strings.HasSuffix(c.Name, "session-token") {
				clearedAccess = true
			}
			if strings.HasSuffix(c.Name, "refresh-token") {
				clearedRefresh = true
			}
		}
	}
	if !clearedAccess {
		t.Error("access cookie not cleared")
	}
	if !clearedRefresh {
		t.Error("refresh cookie not cleared")
	}
}

// safeRedirectPath is the test mirror of the frontend's safeRedirectPath
// — same rules. Tests live in this file because we can't easily get to
// the React component from Go, but we can prove the *server-side*
// equivalent (NextAuthHandler.SignIn) follows the same rules.
func TestSignIn_OpenRedirectBlocked(t *testing.T) {
	h, _, _, _, _ := newSignOutRig(t)

	cases := []struct {
		name     string
		callback string
		want     string
	}{
		{"protocol-relative", "//evil.example.com", "/login?callbackUrl=%2F"},
		{"absolute http", "http://evil.example.com", "/login?callbackUrl=%2F"},
		{"absolute https", "https://evil.example.com", "/login?callbackUrl=%2F"},
		{"backslash trick", "/\\evil", "/login?callbackUrl=%2F%5Cevil"},
		{"javascript scheme", "javascript:alert(1)", "/login?callbackUrl=%2F"},
		{"safe relative", "/missions/abc", "/login?callbackUrl=%2Fmissions%2Fabc"},
		{"empty defaults to /", "", "/login?callbackUrl=%2F"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/auth/signin?callbackUrl="+c.callback, nil)
			rr := httptest.NewRecorder()
			h.SignIn(rr, req)

			loc := rr.Header().Get("Location")
			if loc != c.want {
				t.Errorf("got Location=%q, want %q", loc, c.want)
			}
		})
	}
}
