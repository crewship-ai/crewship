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
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestRouteWithRateLimiting_AuthPath_ValidCLIToken_StillAuthRateLimited(t *testing.T) {
	// The stricter auth bucket (10/min on /api/auth/*, /api/v1/auth/*,
	// /api/v1/bootstrap) currently wins over the CLI exemption only because
	// its branch sits EARLIER in routeWithRateLimiting — the exemption
	// branch's `/api/` prefix check would match auth paths too. This test
	// pins that ordering: reordering the branches so a valid CLI token
	// bypasses the auth limiter (a credential-stuffing shield) must go red.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	plaintext := cliTokenStandardPrefix + "rlauthpath0011223344556677"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('rl-authpath-1', ?, 'rl-test', ?, 'STANDARD', datetime('now'))`,
		userID, hashStandard(plaintext))

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	saw429 := false
	for i := 0; i < 15; i++ { // auth bucket is 10/min (burst 10)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		req.RemoteAddr = "127.0.0.5:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Error("a valid CLI token must NOT exempt auth-path traffic from the 10/min auth bucket — the #1333 exemption is scoped to the general API bucket only")
	}
}

func TestRouteWithRateLimiting_SpoofedToken_DBLookupNegativeCached(t *testing.T) {
	// The exemption's validity check runs before the limiter, so without a
	// damper every spoofed `crewship_cli_…` bearer would force an
	// unthrottled hash+DB lookup per request (DoS amplification). Repeated
	// requests with the SAME fake token must hit the DB exactly once per
	// TTL window — the negative cache absorbs the rest.
	db := setupTestDB(t)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	lookups := 0
	prev := cliExemptDBLookupHook
	cliExemptDBLookupHook = func() { lookups++ } // requests are issued sequentially below, no extra locking needed
	t.Cleanup(func() { cliExemptDBLookupHook = prev })

	fake := cliTokenStandardPrefix + "neg-cache-never-issued-00"
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, rlProbePath, nil)
		req.Header.Set("Authorization", "Bearer "+fake)
		req.RemoteAddr = "127.0.0.6:1"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
	}
	if lookups != 1 {
		t.Errorf("got %d DB lookups for 50 requests with the same spoofed token, want exactly 1 (negative cache must absorb repeats within the TTL)", lookups)
	}
}

func TestRouteWithRateLimiting_ValidToken_NeverNegativeCached(t *testing.T) {
	// Positive results must NOT be cached: revocation and expiry have to
	// bite on the very next request. A valid token therefore pays the DB
	// lookup on every request — and once revoked, loses the exemption
	// immediately (after which its failures ARE negative-cached).
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	plaintext := cliTokenStandardPrefix + "rlnocachepos0011223344556677"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('rl-nocachepos-1', ?, 'rl-test', ?, 'STANDARD', datetime('now'))`,
		userID, hashStandard(plaintext))

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	lookups := 0
	prev := cliExemptDBLookupHook
	cliExemptDBLookupHook = func() { lookups++ }
	t.Cleanup(func() { cliExemptDBLookupHook = prev })

	send := func(remote string) {
		req := httptest.NewRequest(http.MethodGet, rlProbePath, nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		req.RemoteAddr = remote
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
	}

	for i := 0; i < 5; i++ {
		send("127.0.0.7:1")
	}
	if lookups != 5 {
		t.Fatalf("got %d DB lookups for 5 valid-token requests, want 5 — a positive result must never be cached, or revocation would lag", lookups)
	}

	execOrFatal(t, db, `UPDATE cli_tokens SET revoked_at = datetime('now') WHERE id = 'rl-nocachepos-1'`)
	send("127.0.0.7:1")
	send("127.0.0.7:1")
	if lookups != 6 {
		t.Errorf("got %d total DB lookups after revocation, want 6 — the first post-revocation request looks up (and fails), the second must be served from the negative cache", lookups)
	}
}

func TestCLIExemptNegCache_TTLAndBoundedEviction(t *testing.T) {
	c := newCLIExemptNegCache()
	now := time.Now()

	key := sha256.Sum256([]byte("token-a"))
	c.put(key, now)
	if !c.has(key, now) {
		t.Fatal("fresh entry must be a hit")
	}
	if c.has(key, now.Add(cliExemptNegTTL+time.Second)) {
		t.Fatal("entry past its TTL must be a miss")
	}

	// Fill to the cap, then insert one more: the map must never exceed
	// cliExemptNegMax — the whole point is a BOUNDED cache an attacker
	// can't grow without limit by cycling unique spoofed tokens.
	for i := 0; i < cliExemptNegMax; i++ {
		c.put(sha256.Sum256([]byte(fmt.Sprintf("token-%d", i))), now)
	}
	overflow := sha256.Sum256([]byte("token-overflow"))
	c.put(overflow, now)
	if got := len(c.entries); got > cliExemptNegMax {
		t.Errorf("cache grew to %d entries, cap is %d", got, cliExemptNegMax)
	}
	if !c.has(overflow, now) {
		t.Error("the just-inserted entry must be present after eviction made room")
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
