package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cli_pair_cov_test.go — remaining Redeem branches: lookup DB error,
// the consume-update failure, the lost-redeem race (RAISE(IGNORE) →
// zero rows → invalid code), the cli_tokens insert failure, plus the
// sanitizeAdapterHint truncation arm. Helpers prefixed covCP.

func covCPFixture(t *testing.T) (*CliPairHandler, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewCliPairHandler(db, newTestLogger())
	return h, userID
}

func covCPSeedPairing(t *testing.T, h *CliPairHandler, userID, code string) {
	t.Helper()
	now := time.Now().UTC()
	execOrFatal(t, h.db, `INSERT INTO cli_pairings
		(id, user_id, code, status, created_at, expires_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		"covcp-"+code, userID, code,
		now.Format(time.RFC3339), now.Add(10*time.Minute).Format(time.RFC3339))
}

func covCPRedeem(h *CliPairHandler, code string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/cli/pair/redeem",
		jsonBody(map[string]string{"code": code}))
	rr := httptest.NewRecorder()
	h.Redeem(rr, req)
	return rr
}

func TestCovCP_Redeem_LookupDBError_500(t *testing.T) {
	h, _ := covCPFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE cli_pairings RENAME TO cp_broken`)
	rr := covCPRedeem(h, "AAAA-BBBB")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCP_Redeem_ConsumeUpdateError_500(t *testing.T) {
	h, userID := covCPFixture(t)
	covCPSeedPairing(t, h, userID, "AAAA-BBBB")
	execOrFatal(t, h.db, `CREATE TRIGGER covcp_block_upd BEFORE UPDATE ON cli_pairings
		BEGIN SELECT RAISE(ABORT, 'covcp forced'); END`)
	rr := covCPRedeem(h, "AAAABBBB")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovCP_Redeem_LostRace_400 — both redeemers pass the SELECT but
// only one UPDATE lands; the loser (zero rows) must get the same
// "invalid code" answer, never a second token.
func TestCovCP_Redeem_LostRace_400(t *testing.T) {
	h, userID := covCPFixture(t)
	covCPSeedPairing(t, h, userID, "AAAA-BBBB")
	execOrFatal(t, h.db, `CREATE TRIGGER covcp_ignore_upd BEFORE UPDATE ON cli_pairings
		BEGIN SELECT RAISE(IGNORE); END`)
	rr := covCPRedeem(h, "AAAA-BBBB")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Invalid or expired code") {
		t.Errorf("body = %s", rr.Body.String())
	}
	var tokens int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id = ?`, userID).Scan(&tokens); err != nil || tokens != 0 {
		t.Errorf("cli_tokens = %d err=%v, want 0 (loser must not mint)", tokens, err)
	}
}

func TestCovCP_Redeem_TokenInsertError_500(t *testing.T) {
	h, userID := covCPFixture(t)
	covCPSeedPairing(t, h, userID, "AAAA-BBBB")
	execOrFatal(t, h.db, `CREATE TRIGGER covcp_block_tok BEFORE INSERT ON cli_tokens
		BEGIN SELECT RAISE(ABORT, 'covcp forced'); END`)
	rr := covCPRedeem(h, "aaaa bbbb") // normalization also exercised
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// Tx rollback: the pairing must still be pending.
	var status string
	if err := h.db.QueryRow(`SELECT status FROM cli_pairings WHERE code = 'AAAA-BBBB'`).Scan(&status); err != nil || status != "pending" {
		t.Errorf("pairing status = %q err=%v, want pending after rollback", status, err)
	}
}

func TestCovCP_SanitizeAdapterHint_Truncates(t *testing.T) {
	long := strings.Repeat("A", 40)
	got := sanitizeAdapterHint(long)
	if got != strings.Repeat("A", 32) {
		t.Errorf("sanitizeAdapterHint(40xA) = %q, want 32xA", got)
	}
	if sanitizeAdapterHint("we!rd") != "" {
		t.Errorf("invalid chars must strip the hint entirely")
	}
}
