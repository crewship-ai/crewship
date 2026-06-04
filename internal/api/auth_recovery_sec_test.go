package api

import (
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// ---------------------------------------------------------------------------
// auth_recovery_sec_test.go — behavior-preservation guard for the password
// reset path after removing the tautological "constant-time" self-compare
// (storedHash := tokenHash; ConstantTimeCompare(tokenHash, storedHash)).
//
// The real authorization gate is the parameterized
// `WHERE token = ? AND purpose = 'password_reset'` lookup. These tests pin
// the contract that gate enforces so a future cleanup can't silently weaken
// it: a valid token still resets, an unknown token is still rejected.
//
// Reuses the existing harness from auth_recovery_cov_test.go: setupTestDB,
// seedTestUserWithPassword, newRecoveryHandler, covARMintToken, covARPost,
// sessions.NewDBStore.
// ---------------------------------------------------------------------------

// Valid, live token → password reset succeeds (200). Removing the dead
// compare must not break the happy path.
func TestSecRecovery_ValidToken_Resets(t *testing.T) {
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	seedTestUserWithPassword(t, db, "sec-valid@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, store)
	covARMintToken(t, h, "sec-valid@example.com", "sec-valid-raw-token-001")

	req, rr := covARPost("POST", "/api/v1/auth/reset",
		`{"token":"sec-valid-raw-token-001","new_password":"brandnewpw12"}`)
	h.Reset(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200 for a valid token", rr.Code, rr.Body.String())
	}

	// Token must be burned: a replay with the same token now fails.
	req2, rr2 := covARPost("POST", "/api/v1/auth/reset",
		`{"token":"sec-valid-raw-token-001","new_password":"anotherpw123"}`)
	h.Reset(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("replay status = %d, want 400 (token must be single-use)", rr2.Code)
	}
}

// Unknown / never-issued token → rejected (400). The SQL WHERE token = ?
// is the gate; the dead self-compare was never doing this work.
func TestSecRecovery_InvalidToken_Rejected(t *testing.T) {
	db := setupTestDB(t)
	seedTestUserWithPassword(t, db, "sec-invalid@example.com", "originalpw")
	h := newRecoveryHandler(t, db, &stubMailer{configured: true}, nil)

	req, rr := covARPost("POST", "/api/v1/auth/reset",
		`{"token":"this-token-was-never-issued","new_password":"brandnewpw12"}`)
	h.Reset(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown token", rr.Code)
	}
}
