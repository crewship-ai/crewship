package api

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// cli_token_cov_test.go — remaining branches: adminHMACKey's env
// validation, scopesPermittedByRole's tier table, nullIfBlank, plus
// ValidateCLITokenFull's admin path (missing key, synchronous audit
// insert failure) and expiry/revocation guards. Helpers prefixed
// covCT3 (covCT/covCT2 are taken by crew-template files).

func TestCovCT3_AdminHMACKey_Validation(t *testing.T) {
	t.Setenv(adminTokenHMACKeyEnv, "")
	if _, err := adminHMACKey(); err == nil {
		t.Errorf("unset env: want error")
	}
	t.Setenv(adminTokenHMACKeyEnv, "not-hex!")
	if _, err := adminHMACKey(); err == nil || !strings.Contains(err.Error(), "hex-encoded") {
		t.Errorf("non-hex: err = %v, want hex-encoded complaint", err)
	}
	t.Setenv(adminTokenHMACKeyEnv, "abcd") // 2 bytes — too short
	if _, err := adminHMACKey(); err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Errorf("short key: err = %v, want length complaint", err)
	}
	t.Setenv(adminTokenHMACKeyEnv, strings.Repeat("ab", 32)) // 32 bytes
	key, err := adminHMACKey()
	if err != nil || len(key) != 32 {
		t.Errorf("valid key: (%d, %v), want 32-byte key", len(key), err)
	}
}

func TestCovCT3_ScopesPermittedByRole(t *testing.T) {
	cases := []struct {
		role   string
		scopes []string
		want   string
	}{
		{"VIEWER", nil, ""},
		{"MEMBER", nil, ""}, // unscoped token is always permitted
		{"MEMBER", []string{"agents:write"}, "agents:write"},
		{"MANAGER", []string{"agents:write", "crews:*"}, ""},
		{"MANAGER", []string{"workspace:admin"}, "workspace:admin"},
		{"MANAGER", []string{"*"}, "*"},
		{"ADMIN", []string{"*", "workspace:admin"}, ""},
		{"OWNER", []string{"agents:write"}, ""},
	}
	for _, c := range cases {
		if got := scopesPermittedByRole(c.role, c.scopes); got != c.want {
			t.Errorf("scopesPermittedByRole(%s, %v) = %q, want %q", c.role, c.scopes, got, c.want)
		}
	}
}

func TestCovCT3_NullIfBlank(t *testing.T) {
	if v := nullIfBlank("   "); v.Valid {
		t.Errorf("blank → %v, want NULL", v)
	}
	if v := nullIfBlank("x"); !v.Valid || v.String != "x" {
		t.Errorf("value → %v, want valid 'x'", v)
	}
}

func TestCovCT3_Validate_AdminTokenWithoutKey_Invalid(t *testing.T) {
	t.Setenv(adminTokenHMACKeyEnv, "")
	db := setupTestDB(t)
	_, err := ValidateCLITokenFull(context.Background(), db,
		cliTokenAdminPrefix+"deadbeef", ValidateAuditContext{})
	if err == nil || !strings.Contains(err.Error(), "invalid CLI token") {
		t.Fatalf("err = %v, want invalid CLI token", err)
	}
}

func TestCovCT3_Validate_ExpiredStandardToken_Invalid(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	token := cliTokenStandardPrefix + "0123456789abcdef"
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, expires_at, created_at)
		VALUES ('covct3-t1', ?, 'n', ?, 'STANDARD', ?, datetime('now'))`,
		userID, hashStandard(token), past)
	if _, err := ValidateCLITokenFull(context.Background(), db, token, ValidateAuditContext{}); err == nil {
		t.Fatalf("expired token validated, want error")
	}
}

// TestCovCT3_Validate_AdminAuditInsertFailure_Fatal — the synchronous
// ADMIN audit row failing must fail the auth, not silently grant.
func TestCovCT3_Validate_AdminAuditInsertFailure_Fatal(t *testing.T) {
	keyHex := strings.Repeat("cd", 32)
	t.Setenv(adminTokenHMACKeyEnv, keyHex)
	key, _ := hex.DecodeString(keyHex)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	token := cliTokenAdminPrefix + "fedcba9876543210"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('covct3-t2', ?, 'admin', ?, 'ADMIN', datetime('now'))`,
		userID, hashAdmin(token, key))
	execOrFatal(t, db, `CREATE TRIGGER covct3_block_audit BEFORE INSERT ON cli_token_uses
		BEGIN SELECT RAISE(ABORT, 'covct3 forced'); END`)

	_, err := ValidateCLITokenFull(context.Background(), db, token,
		ValidateAuditContext{RemoteAddr: "127.0.0.1", Path: "/x"})
	if err == nil || !strings.Contains(err.Error(), "audit insert") {
		t.Fatalf("err = %v, want fatal audit-insert failure", err)
	}
}

// TestCovCT3_Validate_AdminToken_HappyPath — with the key set and the
// audit table writable, the ADMIN token validates and leaves a use row.
func TestCovCT3_Validate_AdminToken_HappyPath(t *testing.T) {
	keyHex := strings.Repeat("ef", 32)
	t.Setenv(adminTokenHMACKeyEnv, keyHex)
	key, _ := hex.DecodeString(keyHex)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	token := cliTokenAdminPrefix + "00aa11bb22cc33dd"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('covct3-t3', ?, 'admin', ?, 'ADMIN', datetime('now'))`,
		userID, hashAdmin(token, key))

	res, err := ValidateCLITokenFull(context.Background(), db, token,
		ValidateAuditContext{RemoteAddr: "127.0.0.1", UserAgent: "covct3", Path: "/y"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if res.UserID != userID {
		t.Errorf("user = %q, want %q", res.UserID, userID)
	}
	var uses int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cli_token_uses WHERE token_id = 'covct3-t3'`).Scan(&uses); err != nil || uses != 1 {
		t.Errorf("audit uses = %d err=%v, want exactly 1", uses, err)
	}
}

// TestCovCT3_IsValidCLIToken_MatchesFullValidation — IsValidCLIToken (the
// rate limiter's #1333 exemption check) must agree with ValidateCLITokenFull
// on valid/invalid/revoked/expired tokens.
func TestCovCT3_IsValidCLIToken_MatchesFullValidation(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)

	valid := cliTokenStandardPrefix + "isvalid0011223344"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('covct3-iv1', ?, 'n', ?, 'STANDARD', datetime('now'))`,
		userID, hashStandard(valid))

	revoked := cliTokenStandardPrefix + "isrevoked0011223344"
	now := time.Now().UTC().Format(time.RFC3339)
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, revoked_at, created_at)
		VALUES ('covct3-iv2', ?, 'n', ?, 'STANDARD', ?, datetime('now'))`,
		userID, hashStandard(revoked), now)

	expired := cliTokenStandardPrefix + "isexpired0011223344"
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, expires_at, created_at)
		VALUES ('covct3-iv3', ?, 'n', ?, 'STANDARD', ?, datetime('now'))`,
		userID, hashStandard(expired), past)

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"valid", valid, true},
		{"revoked", revoked, false},
		{"expired", expired, false},
		{"unknown", cliTokenStandardPrefix + "totally-unseen", false},
		{"not-cli-shaped", "some-random-bearer-value", false},
	}
	for _, c := range cases {
		if got := IsValidCLIToken(context.Background(), db, c.token); got != c.want {
			t.Errorf("IsValidCLIToken(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCovCT3_IsValidCLIToken_NoAuditSideEffects — the rate limiter calls
// IsValidCLIToken on every request that looks like a CLI token; it must
// never write to cli_token_uses (ADMIN audit) or bump last_used_at. Those
// side effects belong exclusively to the real ValidateCLITokenFull call
// RequireAuth makes downstream — double-writing would corrupt the ADMIN
// per-use audit trail an incident responder relies on.
func TestCovCT3_IsValidCLIToken_NoAuditSideEffects(t *testing.T) {
	keyHex := strings.Repeat("11", 32)
	t.Setenv(adminTokenHMACKeyEnv, keyHex)
	key, _ := hex.DecodeString(keyHex)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	token := cliTokenAdminPrefix + "noauditsideeffect"
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES ('covct3-nse', ?, 'admin', ?, 'ADMIN', datetime('now'))`,
		userID, hashAdmin(token, key))

	for i := 0; i < 5; i++ {
		if !IsValidCLIToken(context.Background(), db, token) {
			t.Fatalf("call %d: IsValidCLIToken = false, want true", i)
		}
	}
	var uses int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cli_token_uses WHERE token_id = 'covct3-nse'`).Scan(&uses); err != nil {
		t.Fatalf("count uses: %v", err)
	}
	if uses != 0 {
		t.Errorf("cli_token_uses rows = %d after 5 IsValidCLIToken calls, want 0 (no audit side effects)", uses)
	}
}
