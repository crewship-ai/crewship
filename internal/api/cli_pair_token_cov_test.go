package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file raises statement/branch coverage for cli_pair.go and the
// remaining branches in cli_token.go that cli_pair_test.go and
// cli_token_test.go leave uncovered. All test funcs are prefixed
// TestCovCLI; new helpers are prefixed covCLI. Existing helpers
// (setupTestDB, seedTestUser, seedTestWorkspace, newTestLogger,
// withUser, execOrFatal) are reused.
//
// Skipped intentionally: the network/interactive-browser branches of
// the pairing flow live entirely on the frontend (UI displays the
// `crewship login --pair --code=…` snippet and polls); the backend has
// no browser-launch path to exercise here. The fire-and-forget
// last_used_at background goroutine in ValidateCLIToken is also not
// asserted (timing-dependent, best-effort by design).

// covCLIPairHandler builds a CliPairHandler with a quiet logger.
func covCLIPairHandler(t *testing.T, db *sql.DB) *CliPairHandler {
	t.Helper()
	return NewCliPairHandler(db, newTestLogger())
}

// covCLIStartCode runs Start for the given user and returns the issued
// pairing code, failing the test on any non-200.
func covCLIStartCode(t *testing.T, h *CliPairHandler, userID, email string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/cli/pair/start", bytes.NewBufferString(`{}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: email}))
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Start status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp pairStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	return resp.Code
}

// ---------- cli_pair.go: Start ----------

// TestCovCLIPairStart_TolerantOfGarbageBody confirms Start ignores an
// unparseable body (readJSON error is discarded) and still issues a
// code — the UI may POST nothing or junk.
func TestCovCLIPairStart_TolerantOfGarbageBody(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/start", bytes.NewBufferString(`not json at all`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()
	h.Start(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (garbage body tolerated); body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovCLIPairStart_StripsBadAdapterHint feeds a hint that fails
// sanitizeAdapterHint so the stored adapter_hint lands NULL via
// nullableHint("").
func TestCovCLIPairStart_StripsBadAdapterHint(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/start",
		bytes.NewBufferString(`{"adapter_hint":"bad;hint with spaces"}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp pairStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var hint interface{}
	if err := db.QueryRow(`SELECT adapter_hint FROM cli_pairings WHERE code=?`, resp.Code).Scan(&hint); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if hint != nil {
		t.Errorf("adapter_hint = %v, want NULL (bad hint stripped)", hint)
	}
}

// TestCovCLIPairStart_DBError500 closes the DB so the INSERT fails and
// Start returns 500.
func TestCovCLIPairStart_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())
	db.Close() // fault injection

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/start", bytes.NewBufferString(`{}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 after db.Close()", rr.Code)
	}
}

// ---------- cli_pair.go: Poll ----------

func TestCovCLIPairPoll_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/cli/pair/poll?code=ABCD-EFGH", nil)
	rr := httptest.NewRecorder()
	h.Poll(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// TestCovCLIPairPoll_MissingCode400 hits the empty/invalid-code branch:
// a code that normalizes to "" yields 400.
func TestCovCLIPairPoll_MissingCode400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/cli/pair/poll?code=short", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Poll(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for un-normalizable code", rr.Code)
	}
}

// TestCovCLIPairPoll_PendingHappy: owner polls their own pending code
// and sees status='pending' plus the echoed adapter_hint.
func TestCovCLIPairPoll_PendingHappy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	// Start with a valid hint so the hint.Valid branch in Poll fires.
	startReq := httptest.NewRequest("POST", "/api/v1/cli/pair/start",
		bytes.NewBufferString(`{"adapter_hint":"CLAUDE_CODE"}`))
	startReq = startReq.WithContext(withUser(startReq.Context(), &AuthUser{ID: userID}))
	startRR := httptest.NewRecorder()
	h.Start(startRR, startReq)
	var start pairStartResponse
	if err := json.Unmarshal(startRR.Body.Bytes(), &start); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/cli/pair/poll?code="+start.Code, nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Poll(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp pairPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "pending" {
		t.Errorf("status = %q, want pending", resp.Status)
	}
	if resp.AdapterHint != "CLAUDE_CODE" {
		t.Errorf("adapter_hint = %q, want CLAUDE_CODE", resp.AdapterHint)
	}
}

// TestCovCLIPairPoll_PendingButExpiredFlipsToExpired seeds a pending row
// whose expires_at is in the past; Poll should report 'expired' AND
// best-effort flip the row's status in the DB.
func TestCovCLIPairPoll_PendingButExpiredFlipsToExpired(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	created := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	execOrFatal(t, db,
		`INSERT INTO cli_pairings (id, user_id, code, status, created_at, expires_at) VALUES ('pexp', ?, 'EXPI-RED1', 'pending', ?, ?)`,
		userID, created, past)

	req := httptest.NewRequest("GET", "/api/v1/cli/pair/poll?code=EXPI-RED1", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Poll(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp pairPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "expired" {
		t.Errorf("reported status = %q, want expired", resp.Status)
	}
	// Best-effort flip persisted.
	var dbStatus string
	if err := db.QueryRow(`SELECT status FROM cli_pairings WHERE code='EXPI-RED1'`).Scan(&dbStatus); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if dbStatus != "expired" {
		t.Errorf("persisted status = %q, want expired (best-effort flip)", dbStatus)
	}
}

// TestCovCLIPairPoll_ConsumedStatus seeds a consumed row and confirms
// Poll passes the status through verbatim (not the pending-expiry
// branch).
func TestCovCLIPairPoll_ConsumedStatus(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	execOrFatal(t, db,
		`INSERT INTO cli_pairings (id, user_id, code, status, created_at, expires_at, consumed_at) VALUES ('pcon', ?, 'CONS-UMED', 'consumed', ?, ?, ?)`,
		userID, now, future, now)

	req := httptest.NewRequest("GET", "/api/v1/cli/pair/poll?code=CONS-UMED", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Poll(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp pairPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "consumed" {
		t.Errorf("status = %q, want consumed", resp.Status)
	}
}

// TestCovCLIPairPoll_DBError500 closes the DB so the SELECT (which is
// neither ErrNoRows nor success) returns a generic error → 500.
func TestCovCLIPairPoll_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/cli/pair/poll?code=ABCD-EFGH", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Poll(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 after db.Close()", rr.Code)
	}
}

// ---------- cli_pair.go: Redeem ----------

func TestCovCLIPairRedeem_InvalidJSON400(t *testing.T) {
	db := setupTestDB(t)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem", bytes.NewBufferString(`{bad`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

func TestCovCLIPairRedeem_EmptyCode400(t *testing.T) {
	db := setupTestDB(t)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem", bytes.NewBufferString(`{"code":""}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty code", rr.Code)
	}
}

// TestCovCLIPairRedeem_UnknownCode hits the sql.ErrNoRows lookup branch:
// a well-formed but nonexistent code → 400 "Invalid or expired code".
func TestCovCLIPairRedeem_UnknownCode(t *testing.T) {
	db := setupTestDB(t)
	h := NewCliPairHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem",
		bytes.NewBufferString(`{"code":"ZZZZ-9999"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown code", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Invalid or expired") {
		t.Errorf("body = %s, want 'Invalid or expired'", rr.Body.String())
	}
}

// TestCovCLIPairRedeem_NonPendingStatus seeds a consumed row and tries
// to redeem it → the status != "pending" branch fires (400).
func TestCovCLIPairRedeem_NonPendingStatus(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	execOrFatal(t, db,
		`INSERT INTO cli_pairings (id, user_id, code, status, created_at, expires_at, consumed_at) VALUES ('pnp', ?, 'DONE-0000', 'consumed', ?, ?, ?)`,
		userID, now, future, now)

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem",
		bytes.NewBufferString(`{"code":"DONE-0000"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for non-pending row", rr.Code)
	}
}

// TestCovCLIPairRedeem_HappyPathWithHint exercises the full happy
// redeem: pending row → consumed, a new STANDARD cli_tokens row minted,
// token name derived from the adapter_hint, response carries the raw
// token + user email. Asserts persisted DB state.
func TestCovCLIPairRedeem_HappyPathWithHint(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := covCLIPairHandler(t, db)

	code := covCLIStartCode(t, h, userID, "test@example.com")

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem",
		bytes.NewBufferString(`{"code":"`+code+`","adapter_hint":"GEMINI_CLI"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var resp pairRedeemResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(resp.CliToken, cliTokenPrefix) {
		t.Errorf("cli_token = %q, want %s* prefix", resp.CliToken, cliTokenPrefix)
	}
	if resp.UserID != userID {
		t.Errorf("user_id = %q, want %q", resp.UserID, userID)
	}
	if resp.Email != "test@example.com" {
		t.Errorf("email = %q, want test@example.com", resp.Email)
	}

	// Pairing row consumed.
	var status string
	if err := db.QueryRow(`SELECT status FROM cli_pairings WHERE code=?`, code).Scan(&status); err != nil {
		t.Fatalf("re-read pairing: %v", err)
	}
	if status != "consumed" {
		t.Errorf("pairing status = %q, want consumed", status)
	}

	// Minted cli_tokens row named after the hint, tier STANDARD.
	var name, tier string
	if err := db.QueryRow(`SELECT name, tier FROM cli_tokens WHERE user_id=?`, userID).Scan(&name, &tier); err != nil {
		t.Fatalf("re-read token: %v", err)
	}
	if name != "pair-gemini_cli" {
		t.Errorf("token name = %q, want pair-gemini_cli", name)
	}
	if tier != "STANDARD" {
		t.Errorf("tier = %q, want STANDARD", tier)
	}

	// Minted token actually validates back to the user.
	uid, email, _, vErr := ValidateCLIToken(context.Background(), db, resp.CliToken, ValidateAuditContext{})
	if vErr != nil {
		t.Fatalf("ValidateCLIToken: %v", vErr)
	}
	if uid != userID || email != "test@example.com" {
		t.Errorf("validated (%q,%q), want (%q,test@example.com)", uid, email, userID)
	}
}

// TestCovCLIPairRedeem_HappyPathNoHint covers the tokenName default
// "pair" branch (no adapter_hint supplied).
func TestCovCLIPairRedeem_HappyPathNoHint(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())

	code := covCLIStartCode(t, h, userID, "test@example.com")

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem",
		bytes.NewBufferString(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM cli_tokens WHERE user_id=?`, userID).Scan(&name); err != nil {
		t.Fatalf("re-read token: %v", err)
	}
	if name != "pair" {
		t.Errorf("token name = %q, want default 'pair'", name)
	}
}

// TestCovCLIPairRedeem_DBError500 closes the DB so BeginTx fails and
// Redeem returns 500.
func TestCovCLIPairRedeem_DBError500(t *testing.T) {
	db := setupTestDB(t)
	h := NewCliPairHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem",
		bytes.NewBufferString(`{"code":"ABCD-EFGH"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 after db.Close()", rr.Code)
	}
}

// ---------- cli_pair.go: pure helpers ----------

// TestCovCLIGeneratePairingCode covers both the n==8 (4-4 dash split)
// branch and the generic non-8 branch.
func TestCovCLIGeneratePairingCode(t *testing.T) {
	c8, err := generatePairingCode(8)
	if err != nil {
		t.Fatalf("len 8: %v", err)
	}
	if len(c8) != 9 || !strings.Contains(c8, "-") {
		t.Errorf("8-char code = %q, want XXXX-XXXX shape", c8)
	}
	for _, r := range strings.ReplaceAll(c8, "-", "") {
		if !strings.ContainsRune(pairingCodeAlphabet, r) {
			t.Errorf("code char %q not in alphabet", r)
		}
	}

	c6, err := generatePairingCode(6)
	if err != nil {
		t.Fatalf("len 6: %v", err)
	}
	if len(c6) != 6 || strings.Contains(c6, "-") {
		t.Errorf("6-char code = %q, want 6 chars no dash", c6)
	}
}

// TestCovCLINullableHint pins the "" → nil and non-empty → string
// behaviour of nullableHint.
func TestCovCLINullableHint(t *testing.T) {
	if got := nullableHint(""); got != nil {
		t.Errorf("nullableHint(\"\") = %v, want nil", got)
	}
	if got := nullableHint("CLAUDE_CODE"); got != "CLAUDE_CODE" {
		t.Errorf("nullableHint(\"CLAUDE_CODE\") = %v, want CLAUDE_CODE", got)
	}
}

// ---------- cli_token.go: remaining branches ----------

// TestCovCLITokenCreate_InvalidTier400 covers the tier-validation
// branch (neither STANDARD nor ADMIN).
func TestCovCLITokenCreate_InvalidTier400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"name": "t", "tier": "SUPERUSER"})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid tier", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "STANDARD or ADMIN") {
		t.Errorf("body = %s, want tier hint", rr.Body.String())
	}
}

// TestCovCLITokenCreate_ScopedNoMembership403 covers the scoped-token
// branch where the caller has no workspace_members row at all
// (sql.ErrNoRows → 403).
func TestCovCLITokenCreate_ScopedNoMembership403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db) // no workspace membership seeded
	h := NewCLITokenHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"name": "t", "scopes": []string{"agents:read"}})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for no-membership scoped token", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "no workspace membership") {
		t.Errorf("body = %s, want membership hint", rr.Body.String())
	}
}

// TestCovCLITokenCreate_BlankScopesSkipped covers the inner branch
// where a scope entry is the empty string and is silently skipped, so
// the request resolves as an effectively-unscoped token (200, no role
// lookup).
func TestCovCLITokenCreate_BlankScopesSkipped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db) // deliberately no membership
	h := NewCLITokenHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"name": "t", "scopes": []string{"", "  "}})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (blank scopes skipped → unscoped); body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovCLITokenCreate_AdminTTLTooShort400 covers the ADMIN branch
// where an explicit sub-60s TTL is rejected.
func TestCovCLITokenCreate_AdminTTLTooShort400(t *testing.T) {
	t.Setenv("CREWSHIP_ADMIN_TOKEN_HMAC_KEY",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID) // makes OWNER
	h := NewCLITokenHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{
		"name":               "ops",
		"tier":               "ADMIN",
		"expires_in_seconds": 30,
	})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for sub-60s ADMIN TTL", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "60 seconds") {
		t.Errorf("body = %s, want TTL floor hint", rr.Body.String())
	}
}

// TestCovCLITokenCreate_StandardWithExpiry covers the STANDARD branch
// where body.ExpiresInSeconds > 0 sets expires_at and the response
// echoes it.
func TestCovCLITokenCreate_StandardWithExpiry(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())

	body, _ := json.Marshal(map[string]any{"name": "t", "expires_in_seconds": 3600})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ExpiresAt string `json:"expires_at"`
		Tier      string `json:"tier"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExpiresAt == "" {
		t.Error("STANDARD token with expires_in_seconds must echo expires_at")
	}
	if resp.Tier != "STANDARD" {
		t.Errorf("tier = %q, want STANDARD", resp.Tier)
	}
}

// TestCovCLITokenCreate_DBError500 closes the DB so the final INSERT
// fails and Create returns 500.
func TestCovCLITokenCreate_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())
	db.Close()

	body, _ := json.Marshal(map[string]string{"name": "t"})
	req := httptest.NewRequest("POST", "/api/v1/cli-tokens", bytes.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 after db.Close()", rr.Code)
	}
}

// TestCovCLITokenList_DBError500 closes the DB so the list query fails.
func TestCovCLITokenList_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/cli-tokens", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 after db.Close()", rr.Code)
	}
}

// TestCovCLITokenList_EmptyReturnsEmptyArray covers the tokens==nil →
// [] normalisation branch (user with no tokens).
func TestCovCLITokenList_EmptyReturnsEmptyArray(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/cli-tokens", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Data == nil {
		t.Error("data must be [] not null")
	}
	if len(resp.Data) != 0 {
		t.Errorf("len = %d, want 0", len(resp.Data))
	}
}

// TestCovCLITokenList_EchoesExpiryAndRevoked covers the optional-field
// branches in List (expires_at / last_used_at / revoked_at all valid).
func TestCovCLITokenList_EchoesExpiryAndRevoked(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())

	now := time.Now().UTC().Format(time.RFC3339)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	execOrFatal(t, db,
		`INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, expires_at, last_used_at, revoked_at, created_at)
		 VALUES ('tk-full', ?, 'full', 'hashfull', 'STANDARD', ?, ?, ?, ?)`,
		userID, future, now, now, now)

	req := httptest.NewRequest("GET", "/api/v1/cli-tokens", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len = %d, want 1", len(resp.Data))
	}
	row := resp.Data[0]
	for _, k := range []string{"expires_at", "last_used_at", "revoked_at"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row missing %q (optional-field branch not exercised)", k)
		}
	}
}

// TestCovCLITokenRevoke_DBError500 closes the DB so the UPDATE fails.
func TestCovCLITokenRevoke_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())
	db.Close()

	req := httptest.NewRequest("DELETE", "/api/v1/cli-tokens/whatever", nil)
	req.SetPathValue("tokenId", "whatever")
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Revoke(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 after db.Close()", rr.Code)
	}
}

// TestCovCLITokenValidate_DBError covers ValidateCLIToken's non-ErrNoRows
// DB error branch (wrapped "validate CLI token" error) via a closed DB.
func TestCovCLITokenValidate_DBError(t *testing.T) {
	db := setupTestDB(t)
	db.Close()

	_, _, _, err := ValidateCLIToken(context.Background(), db,
		cliTokenPrefix+"0123456789abcdef", ValidateAuditContext{})
	if err == nil {
		t.Error("expected error from closed DB")
	}
}

// TestCovCLIIsCLIToken_NonPrefix covers the false branch of IsCLIToken
// for a non-CLI bearer-style token (kept distinct from the existing
// tier-acceptance test).
func TestCovCLIIsCLIToken_NonPrefix(t *testing.T) {
	if IsCLIToken("sk_live_abc") {
		t.Error("non-CLI token must not match")
	}
}
