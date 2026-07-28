package api

// Google sign-in is switched off (Pavel, 2026-07-27). Two reasons it is
// worth a test rather than just deleted wiring:
//
//  1. It must stay off even on an instance that still has GOOGLE_CLIENT_ID
//     and GOOGLE_CLIENT_SECRET in its environment — otherwise the feature
//     quietly comes back on the first box with leftover config.
//  2. It created users with NO hashed_password (auth_google.go's INSERT
//     lists email_verified but never a password). That shape is what made
//     the provisioning takeover fix incomplete: an account somebody
//     genuinely controls, holding no password, reads as "unclaimed".
//     Stopping the source is half the fix; see the provisioning tests for
//     the other half, which still matters for accounts already created.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleSignIn_StaysOffEvenWhenConfigured(t *testing.T) {
	// Credentials reach the router through WithGoogleOAuth, not the
	// environment — configuring it via t.Setenv would have made this test
	// pass without ever exercising the enabled path.
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithGoogleOAuth("leftover-client-id", "leftover-secret", "https://crewship.example.com"))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, path := range []string{
		"/api/v1/auth/google/redirect",
		"/api/v1/auth/google/callback?code=x&state=y",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		// A live redirect endpoint is the whole attack surface: it mints
		// accounts. It must not exist, not merely refuse.
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 — Google sign-in must be unreachable", path, rr.Code)
		}
	}
}

func TestGoogleStatus_ReportsDisabledSoTheButtonStaysOff(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithGoogleOAuth("leftover-client-id", "leftover-secret", "https://crewship.example.com"))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/status", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	// The login page renders its Google button from this. Keeping the
	// endpoint (rather than 404ing it) means an older frontend build still
	// gets a definite "off" instead of an error it might render as a
	// spinner forever.
	if body := rr.Body.String(); rr.Code != http.StatusOK || !strings.Contains(body, "false") {
		t.Errorf("status = %d body = %s, want 200 with enabled:false", rr.Code, body)
	}
}
