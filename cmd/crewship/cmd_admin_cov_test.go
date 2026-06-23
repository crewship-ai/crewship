package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/database"
)

// execAdminSQL opens dbURL, applies each statement, and closes again.
// Small helper so seeding fixtures for the admin commands stays one
// line per row at the call site.
func execAdminSQL(t *testing.T, dbURL string, stmts ...string) {
	t.Helper()
	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("open %s: %v", dbURL, err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

// queryAdminString runs a single-row single-column query and returns it
// as a string (NULL → "").
func queryAdminString(t *testing.T, dbURL, query string) string {
	t.Helper()
	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var v *string
	if err := db.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// newAdminCovCmd builds an isolated cobra command bound to the given
// RunE with the full admin flag set, plus an output buffer.
func newAdminCovCmd(runE func(*cobra.Command, []string) error) (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{Use: "test", RunE: runE}
	c.Flags().String("email", "", "")
	c.Flags().String("password", "", "")
	c.Flags().Bool("password-stdin", false, "")
	c.Flags().Bool("active-only", false, "")
	c.Flags().Int("limit", 50, "")
	c.Flags().Bool("locked-only", false, "")
	c.Flags().String("role", "", "")
	c.Flags().String("workspace", "", "")
	buf := new(bytes.Buffer)
	c.SetOut(buf)
	c.SetErr(new(bytes.Buffer))
	return c, buf
}

// ─── admin sessions list ────────────────────────────────────────────

func TestAdminSessionsList_RendersStatuses(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'Alice', 'x')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at, revoked_at, revoked_reason, user_agent, ip)
		 VALUES ('sess-active-00000000', 'u1', datetime('now', '+1 day'), datetime('now', '-1 minute'), NULL, NULL, 'curl/8.0', '10.0.0.1')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at, revoked_at, revoked_reason)
		 VALUES ('sess-revoked', 'u1', datetime('now', '+1 day'), datetime('now', '-2 minute'), datetime('now'), 'user_logout')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at)
		 VALUES ('sess-expired', 'u1', datetime('now', '-1 hour'), datetime('now', '-3 minute'))`,
	)

	cmd, buf := newAdminCovCmd(runAdminSessionsList)
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ID") || !strings.Contains(out, "STATUS") {
		t.Errorf("missing header, got:\n%s", out)
	}
	// 20-char id truncated to 16
	if !strings.Contains(out, "sess-active-0000") || strings.Contains(out, "sess-active-00000000") {
		t.Errorf("expected 16-char truncated id, got:\n%s", out)
	}
	if !strings.Contains(out, "revoked:user_logout") {
		t.Errorf("expected revoked:user_logout row, got:\n%s", out)
	}
	if !strings.Contains(out, "expired") {
		t.Errorf("expected expired row, got:\n%s", out)
	}
	if !strings.Contains(out, "10.0.0.1") || !strings.Contains(out, "curl/8.0") {
		t.Errorf("expected IP and UA cells, got:\n%s", out)
	}
}

func TestAdminSessionsList_ActiveOnlyFiltersInSQL(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'Alice', 'x')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at)
		 VALUES ('s-act', 'u1', '2999-01-01T00:00:00Z', datetime('now'))`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at, revoked_at)
		 VALUES ('s-rev', 'u1', '2999-01-01T00:00:00Z', datetime('now'), datetime('now'))`,
	)

	cmd, buf := newAdminCovCmd(runAdminSessionsList)
	cmd.SetArgs([]string{"--email=a@b.c", "--active-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "s-act") {
		t.Errorf("active session missing, got:\n%s", out)
	}
	if strings.Contains(out, "s-rev") {
		t.Errorf("revoked session should be filtered out, got:\n%s", out)
	}
}

func TestAdminSessionsList_NoSessionsMessage(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', '', 'x')`,
	)

	cmd, buf := newAdminCovCmd(runAdminSessionsList)
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// full_name empty → display name falls back to email
	if !strings.Contains(buf.String(), "(no sessions for a@b.c)") {
		t.Errorf("expected no-sessions message, got:\n%s", buf.String())
	}

	cmd2, buf2 := newAdminCovCmd(runAdminSessionsList)
	cmd2.SetArgs([]string{"--email=a@b.c", "--active-only", "--limit=0"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("execute active-only: %v", err)
	}
	if !strings.Contains(buf2.String(), "(no active sessions for a@b.c)") {
		t.Errorf("expected no-active-sessions message, got:\n%s", buf2.String())
	}
}

func TestAdminSessionsList_Errors(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)

	cmd, _ := newAdminCovCmd(runAdminSessionsList)
	cmd.SetArgs([]string{"--email="})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--email is required") {
		t.Errorf("empty email: got %v", err)
	}

	cmd2, _ := newAdminCovCmd(runAdminSessionsList)
	cmd2.SetArgs([]string{"--email=ghost@b.c"})
	if err := cmd2.Execute(); err == nil || !strings.Contains(err.Error(), `no user with email "ghost@b.c"`) {
		t.Errorf("unknown email: got %v", err)
	}
}

// ─── admin invalidate-sessions ──────────────────────────────────────

func TestAdminInvalidateSessions_RevokesOnlyActive(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'Alice', 'x')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at) VALUES ('s1', 'u1', datetime('now', '+1 day'), datetime('now'))`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at) VALUES ('s2', 'u1', datetime('now', '+2 day'), datetime('now'))`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at, revoked_at, revoked_reason)
		 VALUES ('s3', 'u1', datetime('now', '+1 day'), datetime('now'), datetime('now'), 'user_logout')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at) VALUES ('s4', 'u1', datetime('now', '-1 day'), datetime('now'))`,
	)

	cmd, buf := newAdminCovCmd(runAdminInvalidateSessions)
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "2 active session(s) revoked") {
		t.Errorf("expected 2 revoked, got:\n%s", buf.String())
	}
	for _, id := range []string{"s1", "s2"} {
		reason := queryAdminString(t, dbURL, `SELECT revoked_reason FROM user_sessions WHERE id='`+id+`'`)
		if reason != "admin_invalidate" {
			t.Errorf("session %s revoked_reason = %q, want admin_invalidate", id, reason)
		}
	}
	// Pre-revoked row keeps its original reason; expired row stays untouched.
	if got := queryAdminString(t, dbURL, `SELECT revoked_reason FROM user_sessions WHERE id='s3'`); got != "user_logout" {
		t.Errorf("s3 reason = %q, want user_logout", got)
	}
	if got := queryAdminString(t, dbURL, `SELECT revoked_at FROM user_sessions WHERE id='s4'`); got != "" {
		t.Errorf("expired s4 should not be revoked, got revoked_at=%q", got)
	}
}

func TestAdminInvalidateSessions_NoActiveSessionsMessage(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', '', 'x')`,
	)
	cmd, buf := newAdminCovCmd(runAdminInvalidateSessions)
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "no active sessions") {
		t.Errorf("expected nothing-to-revoke note, got:\n%s", buf.String())
	}
}

func TestAdminInvalidateSessions_UnknownEmail(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	cmd, _ := newAdminCovCmd(runAdminInvalidateSessions)
	cmd.SetArgs([]string{"--email=ghost@b.c"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no user with email") {
		t.Errorf("got %v", err)
	}
}

// ─── openAdminDB resolution ─────────────────────────────────────────

func TestOpenAdminDB_MissingDatabaseFile(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())

	cmd, _ := newAdminCovCmd(runAdminListUsers)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "database not found at") {
		t.Errorf("expected database-not-found error, got %v", err)
	}
}

func TestOpenAdminDB_DataDirResolutionError(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	// Point the data dir under a regular FILE so MkdirAll fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(blocker, "sub"))

	cmd, _ := newAdminCovCmd(runAdminListUsers)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "resolve data dir") {
		t.Errorf("expected resolve-data-dir error, got %v", err)
	}
}

// ─── admin list-users ───────────────────────────────────────────────

func TestAdminListUsers_TableAndLockoutFooter(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password, failed_login_count, locked_until)
		 VALUES ('u1', 'locked@b.c', 'Locky', 'x', 7, '2999-01-01T00:00:00Z')`,
		`INSERT INTO users (id, email, full_name, hashed_password, locked_until)
		 VALUES ('u2', 'stale@b.c', '', 'x', '2001-01-01T00:00:00Z')`,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u3', 'fine@b.c', 'Fine', 'x')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Acme', 'acme')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', 'w1', 'u3', 'OWNER')`,
	)

	cmd, buf := newAdminCovCmd(runAdminListUsers)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "LOCKED until 2999-01-01 00:00") {
		t.Errorf("expected active lockout cell, got:\n%s", out)
	}
	if !strings.Contains(out, "expired 2001-01-01 00:00") {
		t.Errorf("expected expired lockout cell, got:\n%s", out)
	}
	if !strings.Contains(out, "OWNER@acme") {
		t.Errorf("expected role@slug cell, got:\n%s", out)
	}
	if !strings.Contains(out, "(no workspace)") {
		t.Errorf("expected no-workspace marker, got:\n%s", out)
	}
	if !strings.Contains(out, "1 account(s) currently locked out") {
		t.Errorf("expected lockout footer, got:\n%s", out)
	}
}

func TestAdminListUsers_LockedOnlyAndEmpty(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)

	// Empty DB → bootstrap hint.
	cmd, buf := newAdminCovCmd(runAdminListUsers)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	if !strings.Contains(buf.String(), "(no users") {
		t.Errorf("expected empty-db message, got:\n%s", buf.String())
	}

	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'fine@b.c', 'Fine', 'x')`,
	)
	cmd2, buf2 := newAdminCovCmd(runAdminListUsers)
	cmd2.SetArgs([]string{"--locked-only"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("execute locked-only: %v", err)
	}
	if !strings.Contains(buf2.String(), "(no currently locked-out users)") {
		t.Errorf("expected locked-only empty message, got:\n%s", buf2.String())
	}
	if strings.Contains(buf2.String(), "fine@b.c") {
		t.Errorf("healthy user must be filtered by --locked-only, got:\n%s", buf2.String())
	}
}

// ─── admin promote ──────────────────────────────────────────────────

func TestAdminPromote_InvalidRole(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	cmd, _ := newAdminCovCmd(runAdminPromote)
	cmd.SetArgs([]string{"--email=a@b.c", "--role=GOD"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `invalid role "GOD"`) {
		t.Errorf("got %v", err)
	}
}

func TestAdminPromote_UnknownEmail(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	cmd, _ := newAdminCovCmd(runAdminPromote)
	cmd.SetArgs([]string{"--email=ghost@b.c", "--role=owner"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no user with email") {
		t.Errorf("got %v", err)
	}
}

func TestAdminPromote_NoMembership(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
	)
	cmd, _ := newAdminCovCmd(runAdminPromote)
	cmd.SetArgs([]string{"--email=a@b.c", "--role=ADMIN"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "has no workspace memberships") {
		t.Errorf("got %v", err)
	}
}

func TestAdminPromote_SingleWorkspaceSuccess(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Acme', 'acme')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', 'w1', 'u1', 'MEMBER')`,
	)
	cmd, buf := newAdminCovCmd(runAdminPromote)
	cmd.SetArgs([]string{"--email=a@b.c", "--role=owner"}) // lowercase upcased
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := queryAdminString(t, dbURL, `SELECT role FROM workspace_members WHERE id='m1'`); got != "OWNER" {
		t.Errorf("role = %q, want OWNER", got)
	}
	if !strings.Contains(buf.String(), `Promoted a@b.c to OWNER in workspace "Acme" (acme)`) {
		t.Errorf("output:\n%s", buf.String())
	}
}

func TestAdminPromote_MultiWorkspaceRequiresFlag(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Acme', 'acme')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('w2', 'Beta', 'beta')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', 'w1', 'u1', 'MEMBER')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m2', 'w2', 'u1', 'MEMBER')`,
	)
	cmd, _ := newAdminCovCmd(runAdminPromote)
	cmd.SetArgs([]string{"--email=a@b.c", "--role=ADMIN"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "multiple workspaces") {
		t.Errorf("got %v", err)
	}

	// Disambiguate with --workspace.
	cmd2, _ := newAdminCovCmd(runAdminPromote)
	cmd2.SetArgs([]string{"--email=a@b.c", "--role=ADMIN", "--workspace=beta"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("explicit workspace: %v", err)
	}
	if got := queryAdminString(t, dbURL, `SELECT role FROM workspace_members WHERE id='m2'`); got != "ADMIN" {
		t.Errorf("w2 role = %q, want ADMIN", got)
	}
	if got := queryAdminString(t, dbURL, `SELECT role FROM workspace_members WHERE id='m1'`); got != "MEMBER" {
		t.Errorf("w1 role = %q, want untouched MEMBER", got)
	}
}

func TestAdminPromote_WorkspaceSlugErrors(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Acme', 'acme')`,
	)
	cmd, _ := newAdminCovCmd(runAdminPromote)
	cmd.SetArgs([]string{"--email=a@b.c", "--role=ADMIN", "--workspace=ghost"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `no workspace with slug "ghost"`) {
		t.Errorf("unknown slug: got %v", err)
	}

	// Workspace exists but the user is not a member → RowsAffected == 0.
	cmd2, _ := newAdminCovCmd(runAdminPromote)
	cmd2.SetArgs([]string{"--email=a@b.c", "--role=ADMIN", "--workspace=acme"})
	if err := cmd2.Execute(); err == nil || !strings.Contains(err.Error(), `not a member of workspace "acme"`) {
		t.Errorf("not-a-member: got %v", err)
	}
}

// ─── admin reset-password extras ────────────────────────────────────

func TestAdminResetPassword_EmailRequired(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	cmd, _ := newAdminCovCmd(runAdminResetPassword)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--email is required") {
		t.Errorf("got %v", err)
	}
}

func TestAdminResetPassword_PasswordStdin(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	hash, err := bcrypt.GenerateFromPassword([]byte("oldpassword"), 4)
	if err != nil {
		t.Fatal(err)
	}
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', '`+string(hash)+`')`,
	)

	cmd, _ := newAdminCovCmd(runAdminResetPassword)
	cmd.SetIn(strings.NewReader("stdin-password-42\n"))
	cmd.SetArgs([]string{"--email=a@b.c", "--password-stdin"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := queryAdminString(t, dbURL, `SELECT hashed_password FROM users WHERE id='u1'`)
	if err := bcrypt.CompareHashAndPassword([]byte(got), []byte("stdin-password-42")); err != nil {
		t.Errorf("stdin password not applied: %v", err)
	}
}

func TestAdminResetPassword_StdinAndFlagMutuallyExclusive(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	cmd, _ := newAdminCovCmd(runAdminResetPassword)
	cmd.SetArgs([]string{"--email=a@b.c", "--password=abcdefgh1", "--password-stdin"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("got %v", err)
	}
}

func TestAdminResetPassword_PromptFailsWithoutTerminal(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
	)
	// No --password, no --password-stdin → falls into promptPasswordTwice,
	// which refuses to prompt when os.Stdin is not a TTY (go test runs
	// with stdin = /dev/null).
	cmd, _ := newAdminCovCmd(runAdminResetPassword)
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "stdin is not a terminal") {
		t.Errorf("got %v", err)
	}
}

func TestPromptPasswordTwice_NonInteractive(t *testing.T) {
	// Direct call: under go test stdin is not a terminal, so the
	// guard clause must fire instead of blocking on a read.
	if _, err := promptPasswordTwice(); err == nil || !strings.Contains(err.Error(), "stdin is not a terminal") {
		t.Errorf("got %v", err)
	}
}

func TestAdminSessionsList_LongUserAgentTruncated(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	longUA := strings.Repeat("M", 40)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
		`INSERT INTO user_sessions (id, user_id, expires_at, created_at, user_agent)
		 VALUES ('s1', 'u1', datetime('now', '+1 day'), datetime('now'), '`+longUA+`')`,
	)
	cmd, buf := newAdminCovCmd(runAdminSessionsList)
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), strings.Repeat("M", 29)+"...") {
		t.Errorf("UA not truncated to 29+ellipsis:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), longUA) {
		t.Errorf("full UA must not appear:\n%s", buf.String())
	}
}

func TestAdminCommands_OpenDBErrorPropagates(t *testing.T) {
	// Broken data dir + no DATABASE_URL → every admin verb fails in
	// openAdminDB with the same resolve error.
	t.Setenv("DATABASE_URL", "")
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(blocker, "sub"))

	for name, runE := range map[string]func(*cobra.Command, []string) error{
		"sessions list":       runAdminSessionsList,
		"invalidate-sessions": runAdminInvalidateSessions,
		"reset-password":      runAdminResetPassword,
		"promote":             runAdminPromote,
	} {
		cmd, _ := newAdminCovCmd(runE)
		args := []string{"--email=a@b.c"}
		if name == "promote" {
			args = append(args, "--role=ADMIN")
		}
		if name == "reset-password" {
			args = append(args, "--password=longenough1")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "resolve data dir") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestOpenAdminDB_DefaultPathOpensExistingDB(t *testing.T) {
	// DATABASE_URL unset; a crewship.db exists under CREWSHIP_DATA_DIR →
	// openAdminDB opens it via the default path. The empty schema then
	// surfaces as a query error from list-users, proving the open ran.
	t.Setenv("DATABASE_URL", "")
	dataDir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, "crewship.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, _ := newAdminCovCmd(runAdminListUsers)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "list users") {
		t.Errorf("expected list-users query error against empty schema, got %v", err)
	}
}
