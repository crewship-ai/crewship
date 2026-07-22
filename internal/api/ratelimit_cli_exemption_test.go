package api

// #1333: authenticated CLI/admin bulk operations (crewship seed, template
// import, bulk-create) fire far more requests than the general 120/min
// per-IP bucket, tripping 429 mid-run and leaving a half-seeded tenant.
// These tests exercise the fix end-to-end through Router.ServeHTTP:
//   - a request bearing a genuinely valid CLI token never hits the per-IP
//     bucket (routeWithRateLimiting in router.go);
//   - unauthenticated / invalid-token traffic on the same path is still
//     throttled exactly as before — the limiter must stay for the
//     unauthenticated / credential-stuffing surface (MustNotDisableRateLimitInProd
//     is unaffected — it only ever gates the global disable flag).

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// rlProbePath is an unregistered /api/v1 path. The test only cares whether
// routeWithRateLimiting lets the request through to r.mux at all (any
// non-429 status, typically 404) — it deliberately avoids depending on any
// specific handler's auth/workspace wiring.
const rlProbePath = "/api/v1/__ratelimit_cli_exemption_probe__"

func TestRouteWithRateLimiting_ValidCLIToken_ExemptFromPerIPBucket(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	plaintext := cliTokenStandardPrefix + "rlexempt0011223344556677"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('rl-exempt-1', ?, 'rl-test', ?, 'STANDARD', datetime('now'))`,
		userID, hashStandard(plaintext))

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// apiRateLimitedMux is 120/min (burst 120) per IP — 130 requests from a
	// SINGLE IP proves the exemption bypasses the bucket entirely rather
	// than just getting a bigger one.
	for i := 0; i < 130; i++ {
		req := httptest.NewRequest(http.MethodGet, rlProbePath, nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		req.RemoteAddr = "127.0.0.1:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d: got 429 for a valid CLI-token request, want exemption from the per-IP bucket", i)
		}
	}
}

func TestRouteWithRateLimiting_Unauthenticated_StillRateLimited(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	saw429 := false
	for i := 0; i < 130; i++ {
		req := httptest.NewRequest(http.MethodGet, rlProbePath, nil)
		req.RemoteAddr = "127.0.0.2:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Error("unauthenticated traffic must still trip the per-IP limiter — #1333 must not weaken the unauthenticated surface")
	}
}

func TestRouteWithRateLimiting_InvalidCLIToken_StillRateLimited(t *testing.T) {
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// Correctly SHAPED (prefix-matching) CLI token that was never issued —
	// proves the exemption checks real validity (hash lookup), not just
	// the "crewship_cli_" prefix, which would otherwise let an attacker
	// spoof the header to dodge throttling entirely.
	fake := cliTokenStandardPrefix + "never-issued-0011223344"

	saw429 := false
	for i := 0; i < 130; i++ {
		req := httptest.NewRequest(http.MethodGet, rlProbePath, nil)
		req.Header.Set("Authorization", "Bearer "+fake)
		req.RemoteAddr = "127.0.0.3:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Error("a fake/never-issued CLI-token-shaped bearer must still be rate-limited — shape alone must not grant the exemption")
	}
}

func TestRouteWithRateLimiting_CredentialTestPath_ExemptionDoesNotApply(t *testing.T) {
	// The credential-test anti-oracle limiter is intentionally tighter than
	// the general API bucket and applies regardless of auth — #1333 is
	// scoped to bulk write operations, not the credential-validation oracle
	// surface. A valid CLI token must NOT bypass it.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	plaintext := cliTokenStandardPrefix + "rlcredtest0011223344556677"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('rl-credtest-1', ?, 'rl-test', ?, 'STANDARD', datetime('now'))`,
		userID, hashStandard(plaintext))

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	saw429 := false
	for i := 0; i < 70; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/credentials/test", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		req.RemoteAddr = "127.0.0.4:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Error("the credential-test anti-oracle limiter must apply even to a valid CLI token — #1333 is scoped to bulk writes, not credential testing")
	}
}
