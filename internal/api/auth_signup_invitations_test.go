package api

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Signup is where a workspace invitation gets redeemed.
//
// POST /workspaces/{id}/invitations has always been able to name an
// address that has no account yet, but nothing ever consumed the row —
// the invitee signed up and landed in their own private workspace with
// the invitation still pending. Signup is the moment the address proves
// it controls itself (the invitee chose the password), so that is where
// the pending rows are honoured. It is also what lets `crewship seed
// --with-users` place its RBAC fixture without any endpoint that maps an
// arbitrary email to a user id.

// seedInviter creates a workspace and its OWNER, and returns both ids.
func seedInviter(t *testing.T, db *sql.DB) (userID, wsID string) {
	t.Helper()
	userID, wsID = "inviter-user", "inviter-ws"
	if _, err := db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, 'inviter@example.com', 'Inviter')`, userID); err != nil {
		t.Fatalf("seed inviter: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Inviter WS', 'inviter-ws')`, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('inviter-m', ?, ?, 'OWNER')`,
		wsID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return userID, wsID
}

func seedInvitation(t *testing.T, db *sql.DB, id, wsID, inviter, email, role string, expires time.Time, acceptedAt any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO workspace_invitations (id, workspace_id, email, role, invited_by, token, expires_at, accepted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, email, role, inviter, "tok-"+id, expires.UTC().Format(time.RFC3339), acceptedAt); err != nil {
		t.Fatalf("seed invitation %s: %v", id, err)
	}
}

func TestSignup_ConsumesPendingInvitation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	inviter, wsID := seedInviter(t, db)
	seedInvitation(t, db, "inv1", wsID, inviter, "invitee@example.com", "MANAGER",
		time.Now().Add(24*time.Hour), nil)

	rr := signupForEnumTest(t, h, "invitee@example.com")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}

	var role string
	err := db.QueryRow(`
		SELECT wm.role FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ? AND u.email = 'invitee@example.com'`, wsID).Scan(&role)
	if err != nil {
		t.Fatalf("invitee not placed in the inviting workspace: %v", err)
	}
	if role != "MANAGER" {
		t.Errorf("role = %q, want MANAGER (the invited role)", role)
	}

	var acceptedAt sql.NullString
	if err := db.QueryRow(`SELECT accepted_at FROM workspace_invitations WHERE id = 'inv1'`).Scan(&acceptedAt); err != nil {
		t.Fatalf("read invitation: %v", err)
	}
	if !acceptedAt.Valid {
		t.Error("invitation left pending — a second signup attempt would try to redeem it again")
	}

	// The invitee still owns their own default workspace: redeeming an
	// invitation is additive, not a replacement.
	var owned int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE u.email = 'invitee@example.com' AND wm.role = 'OWNER'`).Scan(&owned); err != nil {
		t.Fatalf("count owned: %v", err)
	}
	if owned != 1 {
		t.Errorf("OWNER memberships = %d, want 1", owned)
	}
}

func TestSignup_IgnoresExpiredAndAcceptedInvitations(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	inviter, wsID := seedInviter(t, db)
	seedInvitation(t, db, "inv-expired", wsID, inviter, "late@example.com", "ADMIN",
		time.Now().Add(-1*time.Hour), nil)

	other := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-ws')`, other); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	seedInvitation(t, db, "inv-used", other, inviter, "late@example.com", "ADMIN",
		time.Now().Add(24*time.Hour), "2026-01-01T00:00:00Z")

	if rr := signupForEnumTest(t, h, "late@example.com"); rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}

	var extra int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE u.email = 'late@example.com' AND wm.workspace_id IN (?, ?)`, wsID, other).Scan(&extra); err != nil {
		t.Fatalf("count: %v", err)
	}
	if extra != 0 {
		t.Errorf("placed into %d workspace(s) on expired/already-accepted invitations, want 0", extra)
	}
}

// The collision branch must stay a total no-op. Redeeming invitations
// there would let anyone who knows an invited address join that
// workspace by POSTing signup — the account holder never consented, and
// the extra DB write would also be a timing tell.
func TestSignup_ExistingAccount_DoesNotRedeemInvitations(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	inviter, wsID := seedInviter(t, db)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u-taken', 'taken@example.com', 'Taken', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedInvitation(t, db, "inv2", wsID, inviter, "taken@example.com", "ADMIN",
		time.Now().Add(24*time.Hour), nil)

	if rr := signupForEnumTest(t, h, "taken@example.com"); rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}

	var members int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = 'u-taken'`, wsID).Scan(&members); err != nil {
		t.Fatalf("count: %v", err)
	}
	if members != 0 {
		t.Errorf("collision branch redeemed the invitation (%d rows), want 0", members)
	}
}
