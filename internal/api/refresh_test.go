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

// refreshTestRig packages the moving parts every refresh test needs:
// a NextAuthHandler bound to a real DB, a sessions store, and a freshly
// minted access+refresh cookie pair anchored at a known session row.
type refreshTestRig struct {
	h        *NextAuthHandler
	v        *auth.JWTValidator
	store    sessions.Store
	db       *signinDBHandle
	userID   string
	sessID   string
	access   string
	refresh  string
	hostName string
}

type signinDBHandle struct{ inner interface{ Exec(string, ...any) (any, error) } }

func newRefreshRig(t *testing.T) *refreshTestRig {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	h := NewNextAuthHandler(db, logger, v, store)

	uid := seedTestUser(t, db)
	sess, err := store.Create(context.Background(), uid, "ua", "1.2.3.4", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	access, err := v.IssueAccessToken(uid, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	refresh, err := v.IssueRefreshToken(uid, sess.ID)
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}

	return &refreshTestRig{
		h: h, v: v, store: store,
		userID: uid, sessID: sess.ID, access: access, refresh: refresh,
		hostName: "crewship.test",
	}
}

// post builds a POST /api/auth/token/refresh request with the rig's
// refresh cookie attached. Caller adjusts headers/cookies as the test
// requires before calling the handler.
func (r *refreshTestRig) post(t *testing.T) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	return r.postWithScheme(t, "http", "authjs.refresh-token")
}

// postSecure builds the same request as post() but flagged as TLS so
// the handler picks the __Secure- cookie names. Used to cover the
// HTTPS branch of cookie naming/path logic — the handler does
// different work on each scheme and the test suite needs to exercise
// both.
func (r *refreshTestRig) postSecure(t *testing.T) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	return r.postWithScheme(t, "https", "__Secure-authjs.refresh-token")
}

func (r *refreshTestRig) postWithScheme(t *testing.T, scheme, refreshCookieName string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("POST", scheme+"://"+r.hostName+"/api/auth/token/refresh", nil)
	req.Host = r.hostName
	req.Header.Set("Origin", scheme+"://"+r.hostName)
	if scheme == "https" {
		// httptest.NewRequest doesn't set r.TLS by itself; the handler
		// reads X-Forwarded-Proto OR r.TLS, so we set the header to
		// flag this request as TLS-terminated upstream — same path
		// production reverse proxies (Caddy, nginx) take.
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: r.refresh})
	return req, httptest.NewRecorder()
}

// --- Happy path -------------------------------------------------------------

func TestRefresh_HappyPath_RotatesBothTokens(t *testing.T) {
	rig := newRefreshRig(t)
	req, rr := rig.post(t)
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s), want 200", rr.Code, rr.Body.String())
	}

	cookies := parseSetCookies(rr.Result().Cookies())
	if cookies["authjs.session-token"] == "" {
		t.Error("new access cookie not set")
	}
	if cookies["authjs.refresh-token"] == "" {
		t.Error("new refresh cookie not set")
	}
	if cookies["authjs.refresh-token"] == rig.refresh {
		t.Error("refresh cookie not actually rotated — server returned the same JWE")
	}
}

// HTTPS branch — the handler reads __Secure-authjs.refresh-token on
// TLS connections and writes back __Secure- cookies. A regression in
// either direction (e.g. someone hardcodes the non-secure name in the
// production path) would slip past the rest of the suite, which is
// HTTP-only.
func TestRefresh_HappyPath_HTTPS(t *testing.T) {
	rig := newRefreshRig(t)
	req, rr := rig.postSecure(t)
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("HTTPS refresh status = %d (body: %s), want 200", rr.Code, rr.Body.String())
	}
	cookies := parseSetCookies(rr.Result().Cookies())
	if cookies["__Secure-authjs.session-token"] == "" {
		t.Error("HTTPS branch must set __Secure- access cookie name")
	}
	if cookies["__Secure-authjs.refresh-token"] == "" {
		t.Error("HTTPS branch must set __Secure- refresh cookie name")
	}
	for _, c := range rr.Result().Cookies() {
		if strings.HasPrefix(c.Name, "__Secure-") && !c.Secure {
			t.Errorf("__Secure- cookie %q must have Secure=true", c.Name)
		}
	}
}

func TestRefresh_HappyPath_AdvancesCurrentRefreshJti(t *testing.T) {
	rig := newRefreshRig(t)
	req, rr := rig.post(t)
	rig.h.RefreshToken(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}

	sess, err := rig.store.Get(context.Background(), rig.sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.CurrentRefreshJti == "" {
		t.Error("current_refresh_jti should be set after rotation")
	}
}

// --- Replay detection -------------------------------------------------------

func TestRefresh_ReplayOfOldTokenRevokesEntireSession(t *testing.T) {
	rig := newRefreshRig(t)

	// First rotation succeeds (legitimate user).
	req1, rr1 := rig.post(t)
	rig.h.RefreshToken(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d", rr1.Code)
	}

	// Second rotation with the SAME old refresh cookie — that's the
	// theft signal. Must revoke the session AND respond 401
	// session_revoked.
	req2, rr2 := rig.post(t)
	rig.h.RefreshToken(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "session_revoked") {
		t.Errorf("body should signal session_revoked, got %q", rr2.Body.String())
	}
	wwwAuth := rr2.Header().Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, "session_revoked") {
		t.Errorf("WWW-Authenticate should carry reason: %q", wwwAuth)
	}

	// Verify the session row is now revoked.
	sess, err := rig.store.Get(context.Background(), rig.sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.RevokedAt == nil {
		t.Error("session should be revoked after replay")
	}
}

func TestRefresh_RevokedSessionRejected(t *testing.T) {
	rig := newRefreshRig(t)
	if err := rig.store.Revoke(context.Background(), rig.sessID, sessions.ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	req, rr := rig.post(t)
	rig.h.RefreshToken(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "session_revoked") {
		t.Errorf("body: %s", rr.Body.String())
	}
}

func TestRefresh_ExpiredSessionRejected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, _ := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	store.SetClock(func() time.Time { return time.Now().Add(-2 * auth.RefreshTokenTTL) })

	uid := seedTestUser(t, db)
	sess, _ := store.Create(context.Background(), uid, "", "", auth.RefreshTokenTTL)
	store.SetClock(time.Now) // restore real clock — the row's expires_at is now in the past
	refresh, _ := v.IssueRefreshToken(uid, sess.ID)

	h := NewNextAuthHandler(db, logger, v, store)
	req := httptest.NewRequest("POST", "http://test.local/api/auth/token/refresh", nil)
	req.Host = "test.local"
	req.Header.Set("Origin", "http://test.local")
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: refresh})

	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// --- CSRF defenses ----------------------------------------------------------

func TestRefresh_RejectsGET(t *testing.T) {
	rig := newRefreshRig(t)
	req := httptest.NewRequest("GET", "http://"+rig.hostName+"/api/auth/token/refresh", nil)
	req.Host = rig.hostName
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: rig.refresh})
	rr := httptest.NewRecorder()
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be rejected: got %d, want 405", rr.Code)
	}
	if rr.Header().Get("Allow") != "POST" {
		t.Errorf("Allow header missing or wrong: %q", rr.Header().Get("Allow"))
	}
}

func TestRefresh_RejectsCrossOriginPOST(t *testing.T) {
	rig := newRefreshRig(t)
	req, rr := rig.post(t)
	// Override Origin to a different host — that's the CSRF scenario:
	// browser at evil.com posting to crewship.test. SameSite=Lax stops
	// the cookie from being sent at all in real life, but defense-in-
	// depth on the server matters when the proxy chain misroutes.
	req.Header.Set("Origin", "http://evil.example.com")
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST status = %d, want 403", rr.Code)
	}
}

func TestRefresh_AcceptsRefererWhenOriginAbsent(t *testing.T) {
	rig := newRefreshRig(t)
	req, rr := rig.post(t)
	req.Header.Del("Origin")
	req.Header.Set("Referer", "http://"+rig.hostName+"/login")
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Referer same-origin should pass: got %d (body %s)", rr.Code, rr.Body.String())
	}
}

func TestRefresh_RejectsCrossOriginReferer(t *testing.T) {
	rig := newRefreshRig(t)
	req, rr := rig.post(t)
	req.Header.Del("Origin")
	req.Header.Set("Referer", "http://evil.example.com/path")
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin Referer status = %d, want 403", rr.Code)
	}
}

func TestRefresh_AcceptsWhenBothHeadersAbsent(t *testing.T) {
	// curl / mobile native clients often send neither Origin nor
	// Referer. We accept those — the cookie itself is the gate.
	rig := newRefreshRig(t)
	req, rr := rig.post(t)
	req.Header.Del("Origin")
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("no Origin no Referer should pass: got %d", rr.Code)
	}
}

// --- Cookie / token integrity ----------------------------------------------

func TestRefresh_NoRefreshCookie(t *testing.T) {
	rig := newRefreshRig(t)
	req := httptest.NewRequest("POST", "http://"+rig.hostName+"/api/auth/token/refresh", nil)
	req.Host = rig.hostName
	req.Header.Set("Origin", "http://"+rig.hostName)
	rr := httptest.NewRecorder()
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no cookie status = %d, want 401", rr.Code)
	}
}

func TestRefresh_GarbageRefreshTokenClearsCookies(t *testing.T) {
	rig := newRefreshRig(t)
	req := httptest.NewRequest("POST", "http://"+rig.hostName+"/api/auth/token/refresh", nil)
	req.Host = rig.hostName
	req.Header.Set("Origin", "http://"+rig.hostName)
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: "not-a-jwe"})
	rr := httptest.NewRecorder()
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("garbage token status = %d, want 401", rr.Code)
	}
	// Server must have set Set-Cookie with MaxAge=-1 so the browser
	// drops the dead cookie. Look for it explicitly.
	clearedAccess := false
	clearedRefresh := false
	for _, c := range rr.Result().Cookies() {
		if c.MaxAge == -1 {
			if c.Name == "authjs.session-token" {
				clearedAccess = true
			}
			if c.Name == "authjs.refresh-token" {
				clearedRefresh = true
			}
		}
	}
	if !clearedAccess {
		t.Error("access cookie should have been cleared")
	}
	if !clearedRefresh {
		t.Error("refresh cookie should have been cleared")
	}
}

func TestRefresh_AccessTokenCannotPassAsRefresh(t *testing.T) {
	// Cross-kind smuggling: client tries to use an access JWE in the
	// refresh path. Different HKDF salt → ValidateRefresh fails to
	// decrypt → 401.
	rig := newRefreshRig(t)
	req := httptest.NewRequest("POST", "http://"+rig.hostName+"/api/auth/token/refresh", nil)
	req.Host = rig.hostName
	req.Header.Set("Origin", "http://"+rig.hostName)
	req.AddCookie(&http.Cookie{Name: "authjs.refresh-token", Value: rig.access})
	rr := httptest.NewRecorder()
	rig.h.RefreshToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("access-as-refresh smuggling status = %d, want 401", rr.Code)
	}
}

// --- helpers ---------------------------------------------------------------

func parseSetCookies(cs []*http.Cookie) map[string]string {
	out := make(map[string]string, len(cs))
	for _, c := range cs {
		out[c.Name] = c.Value
	}
	return out
}

// Suppress unused-import warning when the tests change shape.
var _ = json.Marshal
