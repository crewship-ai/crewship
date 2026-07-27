package api

// Managing your own sessions and CLI tokens used to sit in the SAME 10/min
// per-IP bucket as unauthenticated login, because both live under
// /api/v1/auth/. Revoking a token costs two requests (the DELETE plus the
// list refresh), so a user tidying up four stale tokens hit 429 — and the
// Settings screen that lists sessions 429'd alongside it, reporting
// "Couldn't load your sessions" for what was really a throttle.
//
// The strict bucket exists for credential guessing: login, bootstrap,
// password change, minting a new token. Listing and REVOKING your own
// credentials is neither guessable nor privilege-gaining — the worst a
// caller can do by spamming it is delete their own access faster.
//
// These tests pin both halves of that split.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// authBucketLimit is the strict per-IP budget (router.go NewRateLimiter(10)).
// Firing comfortably more than that from one IP separates "strict bucket"
// from "general bucket" without depending on the exact number.
const authBucketProbes = 25

func hammer(t *testing.T, r *Router, method, path string) (got429 bool) {
	t.Helper()
	for i := 0; i < authBucketProbes; i++ {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = fmt.Sprintf("198.51.100.7:%d", 1000+i)
		// Same IP for every probe — the bucket is per-IP, and varying the
		// port must not look like a different client.
		req.RemoteAddr = "198.51.100.7:1234"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			return true
		}
	}
	return false
}

func newRateLimitRouter(t *testing.T) *Router {
	t.Helper()
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func TestRateLimit_SelfServiceManagement_NotOnTheLoginBucket(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"list sessions", http.MethodGet, "/api/v1/auth/sessions"},
		{"revoke a session", http.MethodPost, "/api/v1/auth/sessions/s_abc/revoke"},
		{"list CLI tokens", http.MethodGet, "/api/v1/auth/cli-tokens"},
		{"revoke a CLI token", http.MethodDelete, "/api/v1/auth/cli-tokens/tok_abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRateLimitRouter(t)
			if hammer(t, r, tc.method, tc.path) {
				t.Errorf("%s %s hit 429 within %d requests — still on the strict login bucket",
					tc.method, tc.path, authBucketProbes)
			}
		})
	}
}

func TestRateLimit_CredentialGuessingSurface_StaysStrict(t *testing.T) {
	// These are the reason the strict bucket exists. If a refactor ever
	// widens them, this test is the tripwire.
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"login callback", http.MethodPost, "/api/auth/callback/credentials"},
		{"bootstrap", http.MethodPost, "/api/v1/bootstrap"},
		{"mint a CLI token", http.MethodPost, "/api/v1/auth/cli-token"},
	}
	// NOT listed: POST /api/v1/users/me/password. It lives under
	// /api/v1/users/, so it has always been on the general 120/min bucket
	// rather than this one — it verifies the CURRENT password, which makes
	// it a guessing surface for anyone holding a stolen session. Left as
	// found: tightening it is a security change on its own terms, not a
	// side effect of unblocking token cleanup.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRateLimitRouter(t)
			if !hammer(t, r, tc.method, tc.path) {
				t.Errorf("%s %s survived %d requests from one IP — it must stay strictly limited",
					tc.method, tc.path, authBucketProbes)
			}
		})
	}
}
