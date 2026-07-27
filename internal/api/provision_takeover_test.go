package api

// Two defects found reviewing PR #1506, both introduced by it.
//
// 1. Handing a setup token back for an email that ALREADY has an account is
//    an account-takeover primitive. POST /api/v1/workspaces is authedSelfMut,
//    so any signed-up user can make themselves OWNER of a throwaway
//    workspace, provision victim@company.com into it, receive a live
//    account_setup token in the JSON response, and redeem it at /auth/reset
//    to overwrite the victim's password — no mail is sent, so the victim
//    never learns. The token must never be minted for an account somebody
//    already controls.
//
// 2. Storing full_name as NULL (this PR's own fix for blank rows) locked
//    those accounts out permanently: the login path scanned it into a bare
//    string. Verified live on dev3 — provision, set password via the link,
//    then login answered CredentialsSignin forever.

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// setPasswordForTest does what redeeming a setup link does: puts a bcrypt
// hash on an account that had none.
func setPasswordForTest(t *testing.T, db *sql.DB, email, password string) {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 4)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET hashed_password = ? WHERE email = ?`, string(hashed), email); err != nil {
		t.Fatalf("set password: %v", err)
	}
}

func nowForTest() time.Time { return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC) }

func TestProvision_RefusesToMintForAnAccountThatHasAPassword(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	// A real, credentialed account — someone else's.
	seedTestUserWithPassword(t, h.db, "victim@example.com", "victims-own-password")

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"victim@example.com","role":"MEMBER"}`)
	// Assert the status FIRST. Without this the two checks below pass
	// vacuously on any regression that errors before the mint — a 500 also
	// yields an empty SetupURL and no token row, so the test would keep
	// reporting green while the feature was broken.
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rr.Code, rr.Body.String())
	}
	out := decodeProvision(t, rr)

	// The whole primitive is the token coming back to the caller.
	if out.SetupURL != "" {
		t.Errorf("setup_url returned for an existing credentialed account: %q — that token resets their password", out.SetupURL)
	}

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM verification_tokens WHERE identifier = ? AND purpose = 'account_setup'`,
		"victim@example.com").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("minted %d account_setup token(s) for an account that already has a password", n)
	}

	// And the useful half still has to happen: withholding the token must
	// not mean withholding the membership.
	var members int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = (SELECT id FROM users WHERE email = ?)`,
		wsID, "victim@example.com").Scan(&members); err != nil {
		t.Fatalf("membership count: %v", err)
	}
	if members != 1 {
		t.Errorf("membership rows = %d, want 1 — the person should still have been added", members)
	}
}

func TestProvision_StillReissuesForAnAccountThatNeverSetOne(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	// Provisioned earlier, link lost, never redeemed — nobody controls this
	// account yet, so re-issuing is the useful case and must keep working.
	provisionReq(t, h, userID, wsID, "OWNER", `{"email":"pending@example.com","role":"MEMBER"}`)
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE user_id = (SELECT id FROM users WHERE email = ?)`,
		"pending@example.com"); err != nil {
		t.Fatalf("detach: %v", err)
	}

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"pending@example.com","role":"MEMBER"}`)
	out := decodeProvision(t, rr)
	if out.SetupURL == "" {
		t.Error("no setup_url for an account with no password — the admin cannot get them in")
	}
}

func TestLogin_WorksForAnAccountWithNoFullName(t *testing.T) {
	db := setupTestDB(t)
	// exactly what ProvisionMember writes: no name, no password yet
	if _, err := db.Exec(
		`INSERT INTO users (id, email, full_name, created_at, updated_at) VALUES (?, ?, NULL, ?, ?)`,
		"u-noname", "noname@example.com", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	setPasswordForTest(t, db, "noname@example.com", "TheirOwnPassword123")

	_, fullName, err := checkAndLockoutOnFail(context.Background(), db,
		"noname@example.com", "TheirOwnPassword123", nowForTest())
	if err != nil {
		// A NULL name must not be a failed credential check. Before the fix
		// this was `sql: Scan error … converting NULL to string`, which the
		// caller reports as CredentialsSignin — a permanent, unexplained
		// lockout indistinguishable from a wrong password.
		t.Fatalf("login rejected an account with no name: %v", err)
	}
	if fullName != "" {
		t.Errorf("fullName = %q, want empty for a NULL name", fullName)
	}
}

func TestProvisionedAccount_CanActuallySignIn(t *testing.T) {
	// The end-to-end shape of the live failure: provision without a name,
	// set a password the way the setup link does, then authenticate.
	h, userID, wsID := provisionRig(t)
	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"fresh@example.com","role":"MEMBER"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("provision: %d", rr.Code)
	}
	setPasswordForTest(t, h.db, "fresh@example.com", "ChosenByThem123")

	if _, _, err := checkAndLockoutOnFail(context.Background(), h.db,
		"fresh@example.com", "ChosenByThem123", nowForTest()); err != nil {
		t.Fatalf("a freshly provisioned member could not sign in: %v", err)
	}
}

func TestProvision_RefusesToMintForAnOAuthAccount(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	// Google sign-in created users with email_verified set and NO
	// hashed_password (auth_google.go). Disabling that flow stops new ones,
	// but every deployment that ever had it enabled still holds accounts of
	// this shape — and "has no password" must not read as "nobody owns it".
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name, email_verified, created_at, updated_at)
		 VALUES ('u-oauth', 'oauth@example.com', 'OAuth Person', ?, ?, ?)`,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO accounts (id, userId, type, provider, providerAccountId)
		 VALUES ('acc-1', 'u-oauth', 'oauth', 'google', 'google-uid-1')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"oauth@example.com","role":"MEMBER"}`)
	out := decodeProvision(t, rr)

	if out.SetupURL != "" {
		t.Errorf("setup_url returned for a linked OAuth account: %q — redeeming it sets a password on an account someone signs into with Google", out.SetupURL)
	}
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM verification_tokens WHERE identifier = ? AND purpose = 'account_setup'`,
		"oauth@example.com").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("minted %d token(s) for an OAuth-backed account", n)
	}
}

func TestProvision_RefusesToMintForAVerifiedAccount(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	// email_verified means the address was proven at some point, so somebody
	// has been through a flow with it. Belt to the OAuth braces above: it
	// catches any future path that verifies an address without setting a
	// password.
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name, email_verified, created_at, updated_at)
		 VALUES ('u-verified', 'verified@example.com', NULL, ?, ?, ?)`,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"verified@example.com","role":"MEMBER"}`)
	if out := decodeProvision(t, rr); out.SetupURL != "" {
		t.Errorf("setup_url returned for a verified account: %q", out.SetupURL)
	}
}
