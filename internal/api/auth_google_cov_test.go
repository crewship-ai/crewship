package api

// Coverage for auth_google.go — the full Callback happy path (token
// exchange → userinfo → find-or-create → session mint → cookies →
// redirect) and findOrCreateUser's three identity branches.
//
// The oauth2 library reads its HTTP client from the request context
// (oauth2.HTTPClient), so the tests inject a RoundTripper that serves
// both the token endpoint and Google's userinfo URL from canned JSON —
// no real network, no transport globals touched.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// covGoogleRT serves the token + userinfo endpoints from memory.
type covGoogleRT struct {
	userinfo   string
	tokenCalls int
}

func (rt *covGoogleRT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Request: req}
	resp.Header.Set("Content-Type", "application/json")
	switch {
	case strings.Contains(req.URL.Path, "/token"):
		rt.tokenCalls++
		resp.Body = io.NopCloser(strings.NewReader(
			`{"access_token":"at-123","refresh_token":"rt-456","expires_in":3600,"token_type":"Bearer","id_token":"x"}`))
	case strings.Contains(req.URL.Host, "googleapis.com"):
		resp.Body = io.NopCloser(strings.NewReader(rt.userinfo))
	default:
		resp.StatusCode = 404
		resp.Body = io.NopCloser(strings.NewReader(`{}`))
	}
	return resp, nil
}

// covGoogleCallback fires the Callback with a fresh state row and the
// fake transport wired through the oauth2 context key.
func covGoogleCallback(t *testing.T, h *GoogleAuthHandler, rt http.RoundTripper, state, redirect string) *httptest.ResponseRecorder {
	t.Helper()
	// created_at must be RFC3339 — the handler fails closed on parse
	// errors, and the column's SQL default is not RFC3339-shaped.
	if _, err := h.db.Exec(
		`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, created_at) VALUES (?, '', '', ?, ?)`,
		state, redirect, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=auth-code-1", nil)
	ctx := context.WithValue(req.Context(), oauth2.HTTPClient, &http.Client{Transport: rt})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	return rr
}

func TestGoogleCallbackCov_HappyPath_NewUser(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	rt := &covGoogleRT{userinfo: `{"sub":"goog-sub-1","email":"new@ex.com","email_verified":true,"name":"New User","picture":"http://p/x.png"}`}

	rr := covGoogleCallback(t, h, rt, "state-g-new", "/dashboard")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307; body=%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("Location = %q", loc)
	}
	if rt.tokenCalls != 1 {
		t.Errorf("token exchange calls = %d", rt.tokenCalls)
	}

	// User + linked account created.
	var userID, fullName string
	if err := db.QueryRow(`SELECT id, full_name FROM users WHERE email = 'new@ex.com'`).Scan(&userID, &fullName); err != nil {
		t.Fatalf("query user: %v", err)
	}
	if fullName != "New User" {
		t.Errorf("full_name = %q", fullName)
	}
	var accUser, accessTok string
	if err := db.QueryRow(
		`SELECT userId, access_token FROM accounts WHERE provider = 'google' AND providerAccountId = 'goog-sub-1'`).
		Scan(&accUser, &accessTok); err != nil {
		t.Fatalf("query account: %v", err)
	}
	if accUser != userID || accessTok != "at-123" {
		t.Errorf("account user=%q token=%q", accUser, accessTok)
	}

	// Session row minted + auth cookies set.
	var sessCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ?`, userID).Scan(&sessCount); err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	if sessCount != 1 {
		t.Errorf("sessions = %d, want 1", sessCount)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) < 2 {
		t.Errorf("cookies = %d, want access+refresh", len(cookies))
	}

	// State must be single-use.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM oauth_states WHERE state = 'state-g-new'`).Scan(&n)
	if n != 0 {
		t.Error("state not consumed")
	}
}

func TestGoogleCallbackCov_ExistingAccount_UpdatesTokens(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	userID := seedTestUser(t, db)
	if _, err := db.Exec(`
		INSERT INTO accounts (id, userId, type, provider, providerAccountId, access_token)
		VALUES ('acc-1', ?, 'oauth', 'google', 'goog-sub-2', 'stale-token')`, userID); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	rt := &covGoogleRT{userinfo: `{"sub":"goog-sub-2","email":"test@example.com","name":"Test User"}`}

	rr := covGoogleCallback(t, h, rt, "state-g-exist", "/")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var tok string
	if err := db.QueryRow(`SELECT access_token FROM accounts WHERE id = 'acc-1'`).Scan(&tok); err != nil {
		t.Fatalf("query: %v", err)
	}
	if tok != "at-123" {
		t.Errorf("access_token = %q, want refreshed at-123", tok)
	}
	// No second user row for the same email.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'test@example.com'`).Scan(&n)
	if n != 1 {
		t.Errorf("user rows = %d, want 1", n)
	}
}

func TestGoogleCallbackCov_ExistingEmail_LinksAccount(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	userID := seedTestUser(t, db) // test@example.com
	rt := &covGoogleRT{userinfo: `{"sub":"goog-sub-3","email":"test@example.com","name":"Test User"}`}

	rr := covGoogleCallback(t, h, rt, "state-g-link", "/")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var accUser string
	if err := db.QueryRow(
		`SELECT userId FROM accounts WHERE provider = 'google' AND providerAccountId = 'goog-sub-3'`).Scan(&accUser); err != nil {
		t.Fatalf("query account: %v", err)
	}
	if accUser != userID {
		t.Errorf("linked user = %q, want %q", accUser, userID)
	}
}

func TestGoogleCallbackCov_UnsafeRedirectFallsBackToRoot(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	rt := &covGoogleRT{userinfo: `{"sub":"goog-sub-4","email":"u4@ex.com","name":"U4"}`}

	rr := covGoogleCallback(t, h, rt, "state-g-evil", "https://evil.example/phish")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want / (open-redirect defense)", loc)
	}
}

func TestGoogleCallbackCov_NotEnabled404(t *testing.T) {
	db := setupTestDB(t)
	h := NewGoogleAuthHandler(db, newTestLogger(), nil, nil, "", "", "http://localhost")
	req := httptest.NewRequest("GET", "/cb?state=s&code=c", nil)
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
	rr2 := httptest.NewRecorder()
	h.Redirect(rr2, httptest.NewRequest("GET", "/redir", nil))
	if rr2.Code != http.StatusNotFound {
		t.Errorf("redirect status = %d, want 404", rr2.Code)
	}
}

func TestGoogleRedirectCov_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	req := httptest.NewRequest("GET", "/api/v1/auth/google?redirect=/settings", nil)
	rr := httptest.NewRecorder()
	h.Redirect(rr, req)
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "accounts.google.com") || !strings.Contains(loc, "client_id=test-client-id") {
		t.Errorf("Location = %q", loc)
	}
	// State row persisted with the validated redirect.
	var redirect string
	if err := db.QueryRow(`SELECT redirect_uri FROM oauth_states LIMIT 1`).Scan(&redirect); err != nil {
		t.Fatalf("query state: %v", err)
	}
	if redirect != "/settings" {
		t.Errorf("redirect_uri = %q", redirect)
	}
}

func TestGoogleCallbackCov_GhostSessionRevokedOnIssueFailure(t *testing.T) {
	// Covered implicitly only when Issue* fails — not reachable with a
	// valid validator. Instead pin the sessions-nil 500 branch.
	db := setupTestDB(t)
	logger := newTestLogger()
	v := newTestJWTValidator(t)
	h := NewGoogleAuthHandler(db, logger, v, nil /* sessions */, "cid", "sec", "http://l")
	rt := &covGoogleRT{userinfo: `{"sub":"goog-sub-5","email":"u5@ex.com","name":"U5"}`}
	rr := covGoogleCallback(t, h, rt, "state-g-nosess", "/")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when sessions store missing", rr.Code)
	}
	// User row was still created before the session failure.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'u5@ex.com'`).Scan(&n)
	if n != 1 {
		t.Errorf("user rows = %d", n)
	}
}
