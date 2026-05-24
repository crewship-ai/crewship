//go:build !clionly

package main

import (
	"bytes"
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openInMemoryUsersDB creates a throwaway SQLite DB with just the
// users + user_sessions schema runAdminInvalidateSessions touches.
// We don't run the full migrations because the admin command only
// reads users.email + writes user_sessions; pulling the migration
// chain in would bloat the test runtime for no extra coverage.
func openInMemoryUsersDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			full_name TEXT
		)`,
		`CREATE TABLE user_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			revoked_at TEXT,
			revoked_reason TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// seedUserWithSessions inserts one user and `activeN` active sessions
// + `revokedN` already-revoked sessions. Returns the user id so the
// caller can pass it through if needed.
func seedUserWithSessions(t *testing.T, db *sql.DB, email string, activeN, revokedN int) string {
	t.Helper()
	userID := "u_" + email
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		userID, email, "Test User"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Active rows expire well in the future so the
	// `datetime(expires_at) > datetime(now)` predicate in the
	// invalidate UPDATE counts them.
	farFuture := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	for i := 0; i < activeN; i++ {
		_, err := db.Exec(`INSERT INTO user_sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
			"sess_"+email+"_active_"+strconv.Itoa(i), userID, farFuture)
		if err != nil {
			t.Fatalf("seed active session: %v", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Revoked rows get a past expires_at — they're already revoked,
	// so the expiry doesn't matter for the predicate, but setting a
	// past value avoids ambiguity in any subsequent active-counting
	// queries the test may add.
	pastExpiry := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	for i := 0; i < revokedN; i++ {
		_, err := db.Exec(`INSERT INTO user_sessions (id, user_id, expires_at, revoked_at, revoked_reason) VALUES (?, ?, ?, ?, 'user_logout')`,
			"sess_"+email+"_revoked_"+strconv.Itoa(i), userID, pastExpiry, now)
		if err != nil {
			t.Fatalf("seed revoked session: %v", err)
		}
	}
	return userID
}

// TestAdminInvalidateSessions_Core exercises the SQL surface
// runAdminInvalidateSessions touches without going through cobra or
// the openAdminDB path. The cobra wrapper is a thin shim; the
// interesting logic is the UPDATE statement's WHERE clause:
//
//   - revokes only matching user_id rows
//   - skips sessions already revoked (revoked_at IS NULL filter)
//   - leaves OTHER users' sessions untouched
//   - sets revoked_reason='admin_invalidate' so audit can
//     distinguish from password-change revokes
//
// This test directly applies the same UPDATE so the SQL contract
// stays pinned even if the cobra wrapper is later refactored.
func TestAdminInvalidateSessions_SQLContract(t *testing.T) {
	t.Parallel()
	db := openInMemoryUsersDB(t)

	// Two users, each with 3 active + 1 already-revoked session.
	aliceID := seedUserWithSessions(t, db, "alice@example.com", 3, 1)
	bobID := seedUserWithSessions(t, db, "bob@example.com", 3, 1)

	// Apply the same UPDATE the admin command uses, scoped to Alice.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.ExecContext(context.Background(), `
		UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = 'admin_invalidate'
		WHERE user_id = ? AND revoked_at IS NULL AND datetime(expires_at) > datetime(?)`, now, aliceID, now)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("rows affected: %v", err)
	}
	if got != 3 {
		t.Errorf("revoked = %d, want 3 (only Alice's 3 active sessions)", got)
	}

	// Alice — every session row should now have revoked_at != NULL
	// and the reason set correctly (or the pre-existing reason
	// preserved on the 1 already-revoked row).
	var aliceActiveLeft int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ? AND revoked_at IS NULL`, aliceID).Scan(&aliceActiveLeft); err != nil {
		t.Fatalf("count alice active: %v", err)
	}
	if aliceActiveLeft != 0 {
		t.Errorf("alice active sessions left = %d, want 0", aliceActiveLeft)
	}

	var aliceAdminInvalidated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ? AND revoked_reason = 'admin_invalidate'`, aliceID).Scan(&aliceAdminInvalidated); err != nil {
		t.Fatalf("count admin_invalidate: %v", err)
	}
	if aliceAdminInvalidated != 3 {
		t.Errorf("alice admin_invalidate rows = %d, want 3", aliceAdminInvalidated)
	}

	// Pre-existing revoked session keeps its original reason.
	var alicePreExistingReason string
	if err := db.QueryRow(`SELECT revoked_reason FROM user_sessions WHERE id = 'sess_alice@example.com_revoked_0'`).Scan(&alicePreExistingReason); err != nil {
		t.Fatalf("query pre-existing: %v", err)
	}
	if alicePreExistingReason != "user_logout" {
		t.Errorf("pre-existing revoke reason = %q, want %q (must not be overwritten)", alicePreExistingReason, "user_logout")
	}

	// Bob — completely untouched.
	var bobActiveLeft int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ? AND revoked_at IS NULL`, bobID).Scan(&bobActiveLeft); err != nil {
		t.Fatalf("count bob active: %v", err)
	}
	if bobActiveLeft != 3 {
		t.Errorf("bob active sessions = %d, want 3 (must not be affected by alice's invalidate)", bobActiveLeft)
	}
}

// TestAdminInvalidateSessions_NoActive verifies the zero-row path:
// a user with no active sessions still returns success (0 affected),
// not an error. The CLI prints "(user had no active sessions —
// nothing to revoke)" in that case.
func TestAdminInvalidateSessions_NoActive(t *testing.T) {
	t.Parallel()
	db := openInMemoryUsersDB(t)
	userID := seedUserWithSessions(t, db, "ghost@example.com", 0, 2)

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.ExecContext(context.Background(), `
		UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = 'admin_invalidate'
		WHERE user_id = ? AND revoked_at IS NULL AND datetime(expires_at) > datetime(?)`, now, userID, now)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("rows affected: %v", err)
	}
	if got != 0 {
		t.Errorf("revoked = %d, want 0 (user had no active sessions)", got)
	}
}

// TestAdminInvalidateSessionsCmd_Wiring guards flag registration.
// NOT parallel: this and TestAdminInvalidateSessionsCmd_EmailRequired
// both mutate the shared adminInvalidateSessionsCmd cobra instance
// (flag set + SetOut/SetErr); running in parallel races on shared
// state. Serial cost is sub-millisecond; cheaper than the alternative
// of deep-copying the command per-test.
func TestAdminInvalidateSessionsCmd_Wiring(t *testing.T) {
	if f := adminInvalidateSessionsCmd.Flags().Lookup("email"); f == nil {
		t.Error("missing --email flag")
	}
	// The Use string must be the subcommand path admins type, not
	// something cuter — `crewship admin invalidate-sessions ...`.
	if !strings.Contains(adminInvalidateSessionsCmd.Use, "invalidate-sessions") {
		t.Errorf("Use = %q, want substring \"invalidate-sessions\"", adminInvalidateSessionsCmd.Use)
	}
}

// TestAdminInvalidateSessionsCmd_EmailRequired exercises the flag
// validation. Cobra normally enforces required flags itself; this
// test pins the behaviour so a refactor that drops MarkFlagRequired
// surfaces in tests, not in production.
//
// NOT parallel: see TestAdminInvalidateSessionsCmd_Wiring comment.
func TestAdminInvalidateSessionsCmd_EmailRequired(t *testing.T) {
	// Snapshot prior state so the next test in this file (or a
	// later run via -count=N) doesn't inherit our mutations.
	prevEmail, _ := adminInvalidateSessionsCmd.Flags().GetString("email")
	prevOut := adminInvalidateSessionsCmd.OutOrStdout()
	prevErr := adminInvalidateSessionsCmd.ErrOrStderr()
	t.Cleanup(func() {
		_ = adminInvalidateSessionsCmd.Flags().Set("email", prevEmail)
		adminInvalidateSessionsCmd.SetOut(prevOut)
		adminInvalidateSessionsCmd.SetErr(prevErr)
	})

	// Reset email flag to empty (a sibling test or --count=N rerun
	// may have set it).
	_ = adminInvalidateSessionsCmd.Flags().Set("email", "")
	buf := &bytes.Buffer{}
	adminInvalidateSessionsCmd.SetOut(buf)
	adminInvalidateSessionsCmd.SetErr(buf)

	err := runAdminInvalidateSessions(adminInvalidateSessionsCmd, nil)
	if err == nil {
		t.Fatal("expected --email required error")
	}
	if !strings.Contains(err.Error(), "--email is required") {
		t.Errorf("err = %v, want substring \"--email is required\"", err)
	}
}
