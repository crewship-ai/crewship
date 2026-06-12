package api

// Second coverage pass for auth_google.go: Redirect's DB-error 500 and the
// Callback failure ladder (exchange failure, userinfo transport error,
// userinfo decode error, find-or-create failure, missing/erroring sessions
// store) plus findOrCreateUser's user-lookup and user-insert error wraps.
//
// Reuses the oauth2.HTTPClient context-injection trick from
// auth_google_cov_test.go — no real network, no transport globals.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// covG2RT lets each endpoint be failed independently.
type covG2RT struct {
	tokenStatus  int    // 0 → 200
	userinfoErr  bool   // transport error on the userinfo fetch
	userinfoBody string // body for the userinfo response
}

func (rt *covG2RT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Request: req}
	resp.Header.Set("Content-Type", "application/json")
	switch {
	case strings.Contains(req.URL.Path, "/token"):
		if rt.tokenStatus != 0 {
			resp.StatusCode = rt.tokenStatus
			resp.Body = io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))
			return resp, nil
		}
		resp.Body = io.NopCloser(strings.NewReader(
			`{"access_token":"at-g2","refresh_token":"rt-g2","expires_in":3600,"token_type":"Bearer"}`))
	case strings.Contains(req.URL.Host, "googleapis.com"):
		if rt.userinfoErr {
			return nil, errors.New("userinfo unreachable")
		}
		resp.Body = io.NopCloser(strings.NewReader(rt.userinfoBody))
	default:
		resp.StatusCode = 404
		resp.Body = io.NopCloser(strings.NewReader(`{}`))
	}
	return resp, nil
}

func covG2Callback(t *testing.T, h *GoogleAuthHandler, rt http.RoundTripper, state string) *httptest.ResponseRecorder {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, created_at) VALUES (?, '', '', '/', ?)`,
		state, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=code-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), oauth2.HTTPClient, &http.Client{Transport: rt}))
	rr := httptest.NewRecorder()
	h.Callback(rr, req)
	return rr
}

const covG2Userinfo = `{"sub":"g2-sub","email":"g2@ex.com","email_verified":true,"name":"G Two","picture":""}`

func TestG2_Redirect_DBError500(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/auth/google?redirect=/dash", nil)
	rr := httptest.NewRecorder()
	h.Redirect(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_ExchangeFails400(t *testing.T) {
	h := newTestGoogleHandler(t, setupTestDB(t))
	rr := covG2Callback(t, h, &covG2RT{tokenStatus: 400}, "g2-exch")
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "exchange authorization code") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_UserinfoTransportError502(t *testing.T) {
	h := newTestGoogleHandler(t, setupTestDB(t))
	rr := covG2Callback(t, h, &covG2RT{userinfoErr: true}, "g2-uinfo")
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "fetch user info") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_UserinfoDecodeError502(t *testing.T) {
	h := newTestGoogleHandler(t, setupTestDB(t))
	rr := covG2Callback(t, h, &covG2RT{userinfoBody: `{not-json`}, "g2-decode")
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "decode user info") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_AccountLinkFails500(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	if _, err := db.Exec(`
		CREATE TRIGGER g2_block_accounts BEFORE INSERT ON accounts
		BEGIN SELECT RAISE(ABORT, 'g2 no accounts'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covG2Callback(t, h, &covG2RT{userinfoBody: covG2Userinfo}, "g2-acct")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_UserInsertFails500(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	if _, err := db.Exec(`
		CREATE TRIGGER g2_block_users BEFORE INSERT ON users
		WHEN NEW.email = 'g2@ex.com'
		BEGIN SELECT RAISE(ABORT, 'g2 no users'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covG2Callback(t, h, &covG2RT{userinfoBody: covG2Userinfo}, "g2-user")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_UserLookupError500(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	// accounts lookup misses first; then the users lookup must error
	// (non-ErrNoRows) → "check user" wrap. Renaming users does that
	// without touching the earlier accounts query.
	if _, err := db.Exec(`ALTER TABLE users RENAME TO users_hidden_g2`); err != nil {
		t.Fatalf("rename users: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE users_hidden_g2 RENAME TO users`) })

	rr := covG2Callback(t, h, &covG2RT{userinfoBody: covG2Userinfo}, "g2-lookup")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestG2_Callback_NilSessionsStore500(t *testing.T) {
	db := setupTestDB(t)
	base := newTestGoogleHandler(t, db)
	h := NewGoogleAuthHandler(db, newTestLogger(), base.validator, nil, "cid", "csec", "http://localhost:8080")
	rr := covG2Callback(t, h, &covG2RT{userinfoBody: covG2Userinfo}, "g2-nosess")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// covG2FailStore fails Create; everything else is a stub.
type covG2FailStore struct{}

func (covG2FailStore) Create(context.Context, string, string, string, time.Duration) (*sessions.Session, error) {
	return nil, errors.New("session store down")
}
func (covG2FailStore) Get(context.Context, string) (*sessions.Session, error) { return nil, nil }
func (covG2FailStore) ListActiveForUser(context.Context, string) ([]*sessions.Session, error) {
	return nil, nil
}
func (covG2FailStore) Revoke(context.Context, string, string) error { return nil }
func (covG2FailStore) RevokeAllForUser(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (covG2FailStore) TouchLastUsed(context.Context, string) error                { return nil }
func (covG2FailStore) RotateRefreshJti(context.Context, string, string, string) error { return nil }
func (covG2FailStore) SetClock(func() time.Time)                                  {}

var _ sessions.Store = covG2FailStore{}

func TestG2_Callback_SessionCreateFails500(t *testing.T) {
	db := setupTestDB(t)
	base := newTestGoogleHandler(t, db)
	h := NewGoogleAuthHandler(db, newTestLogger(), base.validator, covG2FailStore{}, "cid", "csec", "http://localhost:8080")
	rr := covG2Callback(t, h, &covG2RT{userinfoBody: covG2Userinfo}, "g2-sessfail")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// The user was still created before the session failure.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'g2@ex.com'`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}
