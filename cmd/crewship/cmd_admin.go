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

func init() {
	adminResetPasswordCmd.Flags().String("email", "", "Email of the user to reset (required)")
	adminResetPasswordCmd.Flags().String("password", "", "New password (if omitted, prompts interactively)")
	_ = adminResetPasswordCmd.MarkFlagRequired("email")

	adminPromoteCmd.Flags().String("email", "", "Email of the user to promote (required)")
	adminPromoteCmd.Flags().String("role", "", "Target role: OWNER | ADMIN | MANAGER (required)")
	adminPromoteCmd.Flags().String("workspace", "", "Workspace slug (defaults to user's only workspace if exactly one)")
	_ = adminPromoteCmd.MarkFlagRequired("email")
	_ = adminPromoteCmd.MarkFlagRequired("role")

	adminCmd.AddCommand(adminResetPasswordCmd)
	adminCmd.AddCommand(adminListUsersCmd)
	adminCmd.AddCommand(adminPromoteCmd)

	rootCmd.AddCommand(adminCmd)
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
	password, _ := cmd.Flags().GetString("password")
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
	// "password reset" while nothing changed.
	if affected, _ := userRes.RowsAffected(); affected != 1 {
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
	revoked, _ := res.RowsAffected()

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
	fmt.Fprintln(tw, "EMAIL\tNAME\tCREATED\tLOCKED\tROLES")

	count := 0
	for rows.Next() {
		var id, email, name, created, locked, roles string
		if err := rows.Scan(&id, &email, &name, &created, &locked, &roles); err != nil {
			return fmt.Errorf("scan user: %w", err)
		}
		lockedDisplay := "-"
		if locked != "" {
			lockedDisplay = locked
		}
		nameDisplay := name
		if nameDisplay == "" {
			nameDisplay = "-"
		}
		rolesDisplay := roles
		if rolesDisplay == "" {
			rolesDisplay = "(no workspace)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", email, nameDisplay, created, lockedDisplay, rolesDisplay)
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate users: %w", err)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if count == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no users — run `crewship seed` or hit POST /api/v1/bootstrap)")
	}
	return nil
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
	if affected, _ := res.RowsAffected(); affected == 0 {
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
