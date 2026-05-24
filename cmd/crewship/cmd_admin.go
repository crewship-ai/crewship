//go:build !clionly

package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
)

// `crewship admin ...` is the operator-on-the-host recovery surface.
// All subcommands here run a direct DB write against the local SQLite
// file, no HTTP. That's deliberate: the most important caller is an
// admin whose account is locked out and whose server may or may not
// be running. Routing the recovery through the same server they're
// recovering would be circular.
//
// The "credential" for these commands is shell access to the host.
// That matches what GitLab (`gitlab-rake gitlab:password:reset`),
// Gitea (`gitea admin user change-password`), Nextcloud (`occ
// user:resetpassword`) and Mattermost (`mmctl user change-password`)
// all do, and it's the right model for a self-hosted product: if you
// can ssh to the box, you ARE the admin.

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Direct DB operations (host-only). Use when locked out of the UI.",
	Long: `Operator commands that bypass the HTTP API and write directly
to the local SQLite database. Requires read+write access to the
data directory (default: ~/.crewship). The server does not need to
be running.

Use these when a user (typically yourself) cannot log in:
  crewship admin reset-password --email=admin@example.com
  crewship admin list-users
  crewship admin promote --email=admin@example.com --role=OWNER`,
}

var adminResetPasswordCmd = &cobra.Command{
	Use:   "reset-password",
	Short: "Reset a user's password (interactive prompt or --password)",
	RunE:  runAdminResetPassword,
}

var adminListUsersCmd = &cobra.Command{
	Use:   "list-users",
	Short: "List every user in the local database",
	RunE:  runAdminListUsers,
}

var adminPromoteCmd = &cobra.Command{
	Use:   "promote",
	Short: "Promote a user to a workspace role (OWNER, ADMIN, MANAGER)",
	RunE:  runAdminPromote,
}

// adminInvalidateSessionsCmd is the "force logout" surface for an
// incident-response flow where a token / cookie is suspected leaked
// but the password is NOT believed compromised. reset-password
// already revokes sessions as a side effect; this command lets the
// operator do JUST the revoke step so the user doesn't have to
// rotate a perfectly-good password too.
//
// Use cases (operator runs from host SSH):
//   - laptop stolen / recovered, want to kill any session that
//     might still be cached on the device;
//   - suspected token leak via Slack / browser history dump;
//   - audit response — periodic "log everyone out of yesterday's
//     sessions" sweep as part of a compliance ritual.
//
// This is intentionally a separate verb rather than a flag on
// reset-password so the audit trail (journal entry + log line) makes
// the intent obvious: "force logout, no password change". A combined
// `--invalidate-sessions-only` flag on reset-password would be
// surprising — reset-password is for password rotation.
var adminInvalidateSessionsCmd = &cobra.Command{
	Use:   "invalidate-sessions",
	Short: "Revoke every active session for a user (no password change)",
	Long: `Force-logout a user without touching their password. The user can
still log in normally after this — they just need to re-authenticate
on every device they were previously signed in on.

Use when you suspect a session token / cookie leak but the password
is believed safe. Mirrors the session-revoke side effect that
'admin reset-password' performs, without forcing a password rotation.

The operation is logged with reason='admin_invalidate' on each
revoked session row so the audit trail distinguishes this from a
password change.`,
	RunE: runAdminInvalidateSessions,
}

// adminSessionsCmd groups the read surface for user_sessions. Splits
// from the existing 'invalidate-sessions' top-level verb because
// invalidate-sessions wants its own journal/log line — burying it
// under a generic 'sessions' verb group would obscure the write
// intent in the audit trail.
var adminSessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Inspect user session state (forensic read of user_sessions)",
}

// adminSessionsListCmd is the forensic read for the user_sessions
// table. Mirrors 'crewship session list' (user-scoped) but for
// ARBITRARY users — admin-only via direct DB access. Column shape
// matches the user-side command so an operator can switch between
// the two surfaces without re-learning the table.
var adminSessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every session for a user (forensic; admin-only)",
	Long: `Dump every row in user_sessions for the user identified by --email.
Use during incident response to answer "what does this user have
active right now?" — paired with 'admin invalidate-sessions' for the
revoke side.

Default shows EVERY session (active, revoked, expired). Pass
--active-only to filter to currently-valid rows (revoked_at IS NULL
AND expires_at > now), which is what 'crewship session list' shows
the user themselves. --limit caps output for users with hundreds of
historic sessions; default 50 mirrors the journal_entries default.`,
	RunE: runAdminSessionsList,
}

func init() {
	adminResetPasswordCmd.Flags().String("email", "", "Email of the user to reset (required)")
	adminResetPasswordCmd.Flags().String("password", "", "New password (leaks to shell history; prefer --password-stdin in CI)")
	adminResetPasswordCmd.Flags().Bool("password-stdin", false, "Read new password from stdin (preferred for CI / scripts — avoids argv leak)")
	_ = adminResetPasswordCmd.MarkFlagRequired("email")

	adminListUsersCmd.Flags().Bool("locked-only", false, "Show only currently locked-out accounts (filter out healthy users)")

	adminPromoteCmd.Flags().String("email", "", "Email of the user to promote (required)")
	adminPromoteCmd.Flags().String("role", "", "Target role: OWNER | ADMIN | MANAGER (required)")
	adminPromoteCmd.Flags().String("workspace", "", "Workspace slug (defaults to user's only workspace if exactly one)")
	_ = adminPromoteCmd.MarkFlagRequired("email")
	_ = adminPromoteCmd.MarkFlagRequired("role")

	adminInvalidateSessionsCmd.Flags().String("email", "", "Email of the user whose sessions should be revoked (required)")
	_ = adminInvalidateSessionsCmd.MarkFlagRequired("email")

	adminSessionsListCmd.Flags().String("email", "", "Email of the user whose sessions to list (required)")
	adminSessionsListCmd.Flags().Bool("active-only", false, "Show only non-revoked, non-expired sessions")
	adminSessionsListCmd.Flags().Int("limit", 50, "Cap on rows returned (default: 50)")
	_ = adminSessionsListCmd.MarkFlagRequired("email")

	adminCmd.AddCommand(adminResetPasswordCmd)
	adminCmd.AddCommand(adminListUsersCmd)
	adminCmd.AddCommand(adminPromoteCmd)
	adminCmd.AddCommand(adminInvalidateSessionsCmd)

	adminSessionsCmd.AddCommand(adminSessionsListCmd)
	adminCmd.AddCommand(adminSessionsCmd)

	rootCmd.AddCommand(adminCmd)
}

// runAdminSessionsList implements the forensic read of user_sessions
// for a single user identified by email. Returns one row per session
// in created_at-DESC order with a STATUS column derived from
// revoked_at + expires_at via classifyAdminSessionRow.
func runAdminSessionsList(cmd *cobra.Command, _ []string) error {
	email, _ := cmd.Flags().GetString("email")
	activeOnly, _ := cmd.Flags().GetBool("active-only")
	limit, _ := cmd.Flags().GetInt("limit")
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return errors.New("--email is required")
	}
	if limit <= 0 {
		limit = 50
	}

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Resolve user first so a typo'd email returns a clear "no user
	// with email X" instead of an empty result that an operator
	// might read as "no sessions".
	var userID, fullName string
	err = db.QueryRowContext(ctx,
		"SELECT id, COALESCE(full_name, '') FROM users WHERE email = ?", email).Scan(&userID, &fullName)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no user with email %q", email)
	}
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}

	query := `
		SELECT id, created_at, expires_at, last_used_at,
		       COALESCE(revoked_at, ''), COALESCE(revoked_reason, ''),
		       COALESCE(user_agent, ''), COALESCE(ip, '')
		FROM user_sessions
		WHERE user_id = ?
		ORDER BY created_at DESC
		LIMIT ?`
	rows, err := db.QueryContext(ctx, query, userID, limit)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tCREATED\tLAST USED\tEXPIRES\tIP\tUA")

	rendered := 0
	hidden := 0
	for rows.Next() {
		var id, createdAt, expiresAt, lastUsedAt, revokedAt, revokedReason, userAgent, ip string
		if err := rows.Scan(&id, &createdAt, &expiresAt, &lastUsedAt, &revokedAt, &revokedReason, &userAgent, &ip); err != nil {
			return fmt.Errorf("scan session: %w", err)
		}
		status := classifyAdminSessionRow(revokedAt, expiresAt, now)
		if activeOnly && status != "active" {
			hidden++
			continue
		}
		statusCell := status
		if status == "revoked" && revokedReason != "" {
			statusCell = "revoked:" + revokedReason
		}
		dispID := id
		if len(dispID) > 16 {
			dispID = dispID[:16]
		}
		dispIP := ip
		if dispIP == "" {
			dispIP = "-"
		}
		dispUA := userAgent
		if dispUA == "" {
			dispUA = "-"
		}
		if len(dispUA) > 32 {
			dispUA = dispUA[:29] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			dispID, statusCell, shortAdminTime(createdAt), shortAdminTime(lastUsedAt), shortAdminTime(expiresAt),
			dispIP, dispUA)
		rendered++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sessions: %w", err)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	displayName := fullName
	if displayName == "" {
		displayName = email
	}
	if rendered == 0 {
		if activeOnly {
			fmt.Fprintf(cmd.OutOrStdout(), "(no active sessions for %s)\n", displayName)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "(no sessions for %s)\n", displayName)
		}
	}
	if activeOnly && hidden > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\n(%d revoked/expired session(s) hidden by --active-only)\n", hidden)
	}
	return nil
}

// classifyAdminSessionRow derives the STATUS cell from the raw
// revoked_at + expires_at columns. Pure function so the unit test
// can exercise every branch without an SQLite fixture.
//
// Boundary: an expires_at equal to `now` is "expired" (not active) —
// the cookie is dead the instant the clock hits expiry. Mirrors the
// session middleware's boundary check.
func classifyAdminSessionRow(rawRevokedAt, rawExpiresAt string, now time.Time) string {
	if strings.TrimSpace(rawRevokedAt) != "" {
		return "revoked"
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(rawExpiresAt))
	if err != nil {
		// SQLite sometimes round-trips RFC3339 as "YYYY-MM-DD HH:MM:SS"
		// (space separator). Same fallback the lockout classifier uses.
		t2, err2 := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(rawExpiresAt))
		if err2 != nil {
			// Unparseable expiry → treat as active rather than
			// claiming "expired" on a server-side bug. Same
			// conservative-miss posture as the lockout classifier.
			return "active"
		}
		t = t2
	}
	if !t.After(now) {
		return "expired"
	}
	return "active"
}

// shortAdminTime renders an RFC3339 / SQLite timestamp as
// "YYYY-MM-DD HH:MM". Empty input → "-". Renamed from a more generic
// "shortTime" to avoid colliding with potential same-named helpers in
// sibling files.
func shortAdminTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "-"
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format("2006-01-02 15:04")
	}
	if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
		return t.UTC().Format("2006-01-02 15:04")
	}
	return raw
}

// runAdminInvalidateSessions revokes every active session for the
// user identified by --email. Returns the count of revoked sessions
// for the success line; treats "user not found" as a loud error
// (better than silent zero rows) and "user found but had no active
// sessions" as success with a "0 active session(s)" message.
//
// The revoked_reason column is set to 'admin_invalidate' so a
// later audit query can distinguish this from password-change
// revokes ('password_change') and self-revokes (user clicked
// "log out from all devices" in the UI, typically 'user_logout').
func runAdminInvalidateSessions(cmd *cobra.Command, _ []string) error {
	email, _ := cmd.Flags().GetString("email")
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return errors.New("--email is required")
	}

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var userID, fullName string
	err = db.QueryRowContext(ctx,
		"SELECT id, COALESCE(full_name, '') FROM users WHERE email = ?", email).Scan(&userID, &fullName)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no user with email %q", email)
	}
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.ExecContext(ctx, `
		UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = 'admin_invalidate'
		WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	revoked, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke sessions rows affected: %w", err)
	}

	displayName := fullName
	if displayName == "" {
		displayName = email
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ Sessions invalidated for %s (%s).\n", displayName, email)
	fmt.Fprintf(cmd.OutOrStdout(), "  %d active session(s) revoked.\n", revoked)
	if revoked == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  (user had no active sessions — nothing to revoke)")
	}
	return nil
}

// openAdminDB opens the SQLite database directly, mirroring the
// resolution logic of the server (default ~/.crewship/crewship.db,
// overridable via DATABASE_URL). Does NOT run migrations — admin
// commands operate on the schema as-is; if the schema is stale,
// that's the server's job to fix, not ours.
func openAdminDB() (*database.DB, error) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return database.Open(dsn)
	}
	dd, err := database.DefaultDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	if _, err := os.Stat(dd.DatabasePath()); err != nil {
		// Only treat ENOENT as "not initialised" — permission denied,
		// I/O error, or symlink-loop should surface verbatim instead
		// of being mis-reported as a missing database. Otherwise the
		// operator chases the wrong fix (`crewship init`) when the
		// real problem is access rights.
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("database not found at %s — set DATABASE_URL or run `crewship init` first", dd.DatabasePath())
		}
		return nil, fmt.Errorf("stat database path %s: %w", dd.DatabasePath(), err)
	}
	return database.Open(dd.DatabaseURL())
}

func runAdminResetPassword(cmd *cobra.Command, _ []string) error {
	email, _ := cmd.Flags().GetString("email")
	passwordFlag, _ := cmd.Flags().GetString("password")
	passwordStdin, _ := cmd.Flags().GetBool("password-stdin")
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return errors.New("--email is required")
	}

	password, _, err := resolvePasswordInput(passwordFlag, passwordStdin, cmd.InOrStdin())
	if err != nil {
		return err
	}

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var userID, fullName string
	err = db.QueryRowContext(ctx,
		"SELECT id, COALESCE(full_name, '') FROM users WHERE email = ?", email).Scan(&userID, &fullName)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no user with email %q", email)
	}
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}

	if password == "" {
		pw, err := promptPasswordTwice()
		if err != nil {
			return err
		}
		password = pw
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Reset password + clear brute-force lockout state — they belong
	// to the same operation. If shell access can reset a password,
	// it can certainly clear a lockout the password change supersedes.
	userRes, err := tx.ExecContext(ctx, `
		UPDATE users
		SET hashed_password = ?, failed_login_count = 0, locked_until = NULL, last_failed_login_at = NULL, updated_at = ?
		WHERE id = ?`, string(hashed), now, userID)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	// Guard against the row being deleted out from under us between
	// the lookup above and this UPDATE — otherwise we'd print
	// "password reset" while nothing changed. Surface RowsAffected
	// errors too so a driver-metadata failure doesn't masquerade as
	// "no rows".
	affected, err := userRes.RowsAffected()
	if err != nil {
		return fmt.Errorf("update password rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("update password: expected 1 row affected, got %d", affected)
	}

	// Revoke every active session so any leaked cookie can't outlive
	// the recovery. The HTTP /reset path does the same via the
	// sessions.Store API; here we have to write directly.
	res, err := tx.ExecContext(ctx, `
		UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = 'password_change'
		WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	revoked, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke sessions rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	displayName := fullName
	if displayName == "" {
		displayName = email
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ Password reset for %s (%s).\n", displayName, email)
	if revoked > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d active session(s) revoked.\n", revoked)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "  Lockout (if any) cleared.")
	return nil
}

func runAdminListUsers(cmd *cobra.Command, _ []string) error {
	lockedOnly, _ := cmd.Flags().GetBool("locked-only")

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, `
		SELECT u.id, u.email, COALESCE(u.full_name, ''), u.created_at,
		       COALESCE(u.locked_until, ''),
		       COALESCE(u.failed_login_count, 0),
		       COALESCE((
		         SELECT GROUP_CONCAT(role || '@' || w.slug, ', ')
		         FROM workspace_members wm
		         JOIN workspaces w ON w.id = wm.workspace_id
		         WHERE wm.user_id = u.id
		       ), '')
		FROM users u
		ORDER BY u.created_at ASC`)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tNAME\tCREATED\tLOCKED\tFAILS\tROLES")

	now := time.Now().UTC()
	activeLockouts := 0
	rendered := 0
	for rows.Next() {
		var id, email, name, created, locked, roles string
		var failed int
		if err := rows.Scan(&id, &email, &name, &created, &locked, &failed, &roles); err != nil {
			return fmt.Errorf("scan user: %w", err)
		}
		isActiveLockout, lockedDisplay := classifyLockoutStatus(locked, now)
		if isActiveLockout {
			activeLockouts++
		}
		if lockedOnly && !isActiveLockout {
			continue
		}
		nameDisplay := name
		if nameDisplay == "" {
			nameDisplay = "-"
		}
		rolesDisplay := roles
		if rolesDisplay == "" {
			rolesDisplay = "(no workspace)"
		}
		failsDisplay := "-"
		if failed > 0 {
			failsDisplay = fmt.Sprintf("%d", failed)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", email, nameDisplay, created, lockedDisplay, failsDisplay, rolesDisplay)
		rendered++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate users: %w", err)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if rendered == 0 {
		if lockedOnly {
			fmt.Fprintln(cmd.OutOrStdout(), "(no currently locked-out users)")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "(no users — run `crewship seed` or hit POST /api/v1/bootstrap)")
		}
	}
	// Footer surfaces the lockout count even when not filtering, so an
	// admin who runs `list-users` casually still notices the brute-force
	// activity without needing to scan the LOCKED column visually.
	if activeLockouts > 0 && !lockedOnly {
		fmt.Fprintf(cmd.OutOrStdout(),
			"\n%s%d account(s) currently locked out.%s Unlock with: crewship admin reset-password --email <email>\n",
			cli.Yellow, activeLockouts, cli.Reset)
	}
	return nil
}

// classifyLockoutStatus inspects a `locked_until` cell from the users
// table and decides whether the account is CURRENTLY locked (vs.
// merely having a stale expired lockout still recorded). Returns:
//
//   - (true, "LOCKED until <ts>")     — locked_until is in the future
//   - (false, "expired <ts>")          — locked_until is in the past
//   - (false, "-")                     — locked_until is empty / null
//   - (false, "<raw>")                 — locked_until is unparseable; raw
//     passes through so the operator
//     can still see what the DB holds
//
// Parse failure deliberately falls through to (false, raw) rather than
// flagging as active — the alternative (claiming "currently locked")
// would pressure the operator to reset-password on accounts whose
// lockout timestamp is a server-side bug, not a real lockout.
//
// The function is pure (takes the raw string + a clock) so the test
// can pin every branch without standing up a database.
func classifyLockoutStatus(rawLockedUntil string, now time.Time) (bool, string) {
	rawLockedUntil = strings.TrimSpace(rawLockedUntil)
	if rawLockedUntil == "" {
		return false, "-"
	}
	t, err := time.Parse(time.RFC3339, rawLockedUntil)
	if err != nil {
		// SQLite sometimes round-trips RFC3339 timestamps as
		// "YYYY-MM-DD HH:MM:SS" (space separator). Try that as a
		// secondary parse before giving up — same fallback the
		// /forgot handler uses on the auth_recovery path.
		t2, err2 := time.Parse("2006-01-02 15:04:05", rawLockedUntil)
		if err2 != nil {
			return false, rawLockedUntil
		}
		t = t2
	}
	if t.After(now) {
		return true, "LOCKED until " + t.UTC().Format("2006-01-02 15:04")
	}
	return false, "expired " + t.UTC().Format("2006-01-02 15:04")
}

func runAdminPromote(cmd *cobra.Command, _ []string) error {
	email, _ := cmd.Flags().GetString("email")
	role, _ := cmd.Flags().GetString("role")
	workspaceSlug, _ := cmd.Flags().GetString("workspace")

	email = strings.ToLower(strings.TrimSpace(email))
	role = strings.ToUpper(strings.TrimSpace(role))

	switch role {
	case "OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER":
	default:
		return fmt.Errorf("invalid role %q — must be OWNER | ADMIN | MANAGER | MEMBER | VIEWER", role)
	}

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var userID string
	if err := db.QueryRowContext(ctx, "SELECT id FROM users WHERE email = ?", email).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user with email %q", email)
		}
		return fmt.Errorf("look up user: %w", err)
	}

	var workspaceID, wsName, wsSlug string
	if workspaceSlug == "" {
		// Default to the user's only workspace, if there's exactly
		// one. Anything else requires an explicit --workspace flag
		// so a multi-workspace user can't accidentally get promoted
		// in the wrong place.
		err = db.QueryRowContext(ctx, `
			SELECT w.id, w.name, w.slug FROM workspaces w
			JOIN workspace_members wm ON wm.workspace_id = w.id
			WHERE wm.user_id = ?
			LIMIT 2`, userID).Scan(&workspaceID, &wsName, &wsSlug)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("user %q has no workspace memberships — bootstrap them first", email)
		}
		if err != nil {
			return fmt.Errorf("resolve workspace: %w", err)
		}
		// Cheap second-row probe: re-run the same query asking for
		// the second match. If it returns rows, ambiguous. sql.ErrNoRows
		// is the happy path here (= "no second workspace = unambiguous");
		// any other error must surface so a transient I/O failure can't
		// silently fall through into a possibly-wrong promotion target.
		var dummy string
		switch err := db.QueryRowContext(ctx, `
			SELECT 'x' FROM workspaces w
			JOIN workspace_members wm ON wm.workspace_id = w.id
			WHERE wm.user_id = ? AND w.id != ?
			LIMIT 1`, userID, workspaceID).Scan(&dummy); {
		case err == nil:
			return errors.New("user belongs to multiple workspaces — pass --workspace=<slug>")
		case errors.Is(err, sql.ErrNoRows):
			// unambiguous — fall through to promotion
		default:
			return fmt.Errorf("resolve workspace ambiguity: %w", err)
		}
	} else {
		err = db.QueryRowContext(ctx, `
			SELECT w.id, w.name, w.slug FROM workspaces w
			WHERE w.slug = ?`, workspaceSlug).Scan(&workspaceID, &wsName, &wsSlug)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no workspace with slug %q", workspaceSlug)
		}
		if err != nil {
			return fmt.Errorf("look up workspace: %w", err)
		}
	}

	res, err := db.ExecContext(ctx, `
		UPDATE workspace_members SET role = ?
		WHERE user_id = ? AND workspace_id = ?`, role, userID, workspaceID)
	if err != nil {
		return fmt.Errorf("update membership: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		// Surface driver-metadata failures instead of mis-reporting
		// them as "user is not a member".
		return fmt.Errorf("update membership rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("user is not a member of workspace %q", wsSlug)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Promoted %s to %s in workspace %q (%s).\n", email, role, wsName, wsSlug)
	return nil
}

// promptPasswordTwice prompts for a password on stdin without echo and
// asks for confirmation. Errors when the two entries don't match.
func promptPasswordTwice() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// In a non-interactive shell (CI, piped input) there is no
		// terminal to read from. Force operators to pass --password
		// explicitly so a non-interactive run can't fall through and
		// silently fail.
		return "", errors.New("stdin is not a terminal — pass --password=<value> for non-interactive use")
	}
	fmt.Fprint(os.Stderr, "New password: ")
	pw1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password (confirm): %w", err)
	}
	// Constant-time compare: even though this CLI tool has a tiny
	// attack surface, naive `==` on two passwords short-circuits at
	// the first differing byte and could theoretically leak prefix
	// match length to a sufficiently noisy local observer. Cheap
	// to do right, no reason not to.
	if subtle.ConstantTimeCompare(pw1, pw2) != 1 {
		return "", errors.New("passwords don't match")
	}
	return string(pw1), nil
}
