package api

// Regression tests for the refresh-logout bug: read-only NextAuth GETs
// (/api/auth/session, /api/auth/csrf) are polled on EVERY dashboard page load
// — at least two per load. They used to share the tight 10/min login
// brute-force bucket, so ~4-5 rapid refreshes drained it; the resulting 429
// was read by the frontend session probe as "logged out" and bounced the user
// to /login.
//
// These GETs carry no credentials, so they now ride the general 120/min API
// bucket instead. Credential-SUBMITTING auth endpoints (login callback, token
// refresh, bootstrap, everything under /api/v1/auth/) must stay on the 10/min
// bucket — the split is method + prefix aware, not a blanket relaxation.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteWithRateLimiting_AuthReadGets_NotThrottledByLoginBucket(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// 30 rapid session/csrf reads from one IP is far past the 10/min login
	// bucket but well under the 120/min API bucket. None may 429 — this is
	// the exact "hammer refresh a few times" path that was logging users out.
	for _, path := range []string{"/api/auth/session", "/api/auth/csrf"} {
		for i := 0; i < 30; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "127.0.0.20:1"
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code == http.StatusTooManyRequests {
				t.Fatalf("%s request %d got 429 — read-only auth GETs must not sit in the 10/min login bucket", path, i)
			}
		}
	}
}

func TestRouteWithRateLimiting_AuthCredentialPost_StillLoginBucketed(t *testing.T) {
	// The relaxation is scoped to read-only GETs. A credential-submitting POST
	// under /api/auth/ (the NextAuth login callback) must still hit the 10/min
	// brute-force bucket — otherwise the split would have opened a
	// credential-stuffing hole.
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	saw429 := false
	for i := 0; i < 15; i++ { // auth bucket is 10/min (burst 10)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/callback/credentials", nil)
		req.RemoteAddr = "127.0.0.21:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Error("POST /api/auth/callback/credentials must stay on the 10/min login bucket — only read-only auth GETs are relaxed")
	}
}
