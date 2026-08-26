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

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
)

// `crewship admin ...` is the operator-on-the-host recovery surface.
// The WRITE subcommands here run a direct DB write against the local
// SQLite file, no HTTP. That's deliberate: the most important caller is
// an admin whose account is locked out and whose server may or may not
// be running. Routing the recovery through the same server they're
// recovering would be circular.
//
// The "credential" for those commands is shell access to the host.
// That matches what GitLab (`gitlab-rake gitlab:password:reset`),
// Gitea (`gitea admin user change-password`), Nextcloud (`occ
// user:resetpassword`) and Mattermost (`mmctl user change-password`)
// all do, and it's the right model for a self-hosted product: if you
// can ssh to the box, you ARE the admin.
//
// What that reasoning never covered is `list-users`, which has an HTTP
// route (GET /api/v1/admin/users) and used it for nothing: it read
// ~/.crewship/crewship.db while the operator's CLI was pointed
// somewhere else entirely, and printed "(no users …)" with exit 0
// against a populated server (#2086). It now reads the server, and
// --local is how you ask for the file instead. Every remaining
// subcommand goes through requireLocalDB, which refuses rather than
// answer for a server it cannot reach — see local_db_target.go.

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Host-side recovery on the local database. Use when locked out of the UI.",
	Long: `Operator commands for the database FILE on this host. Requires
read+write access to the data directory (default: ~/.crewship, or
DATABASE_URL). The server does not need to be running.

Use these when a user (typically yourself) cannot log in:
  crewship admin reset-password --email=admin@example.com --local
  crewship admin promote --email=admin@example.com --role=OWNER --local

'list-users' is the exception: it reads the server the CLI targets
(GET /api/v1/admin/users), scoped to the current workspace. Pass
--local to read the database file on this host instead.

Because "the file I found" and "the server you named" are different
targets, the local-only subcommands refuse to run when --server /
CREWSHIP_SERVER / --profile names a server, unless you pass --local.
Loopback is not an exemption: a server on localhost routinely runs
with DATABASE_URL pointing somewhere other than the default data dir.`,
}

var adminResetPasswordCmd = &cobra.Command{
	Use:   "reset-password",
	Short: "Reset a user's password (interactive prompt or --password)",
	RunE:  runAdminResetPassword,
}

var adminListUsersCmd = &cobra.Command{
	Use:   "list-users",
	Short: "List the users of the workspace on the server the CLI targets",
	Long: `List users.

By default this reads GET /api/v1/admin/users on the server the CLI is
pointed at, scoped to the current workspace — which is what an admin
asking "who is in here?" means, and what this command failed to do for
its whole life before #2086.

--local reads the database file on this host instead. That path lists
EVERY user in the instance, across all workspaces, and adds the LOCKED
and FAILS columns (lockout state is not exposed by the API), so it is
also the only way to answer --locked-only.`,
	RunE: runAdminListUsers,
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
	// Per-command, NOT persistent on adminCmd. Persistent looked tidier and was
	// wrong: `admin` also hosts eight HTTP-only verbs — stats, health, gdpr,
	// prune-legacy, reencrypt, reap-orphan-containers, ratelimits,
	// memory-config — and an inherited --local would have appeared in all their
	// help output, been accepted, and been ignored. That is the same
	// advertise-then-ignore shape this PR exists to remove, and it is the
	// reason cmd_memory_versions.go and cmd_keeper_eval.go declare theirs
	// per-command too. localdb_flag_guard sits over both halves.
	for _, c := range []*cobra.Command{
		adminResetPasswordCmd, adminListUsersCmd, adminPromoteCmd,
		adminInvalidateSessionsCmd, adminSessionsListCmd,
	} {
		c.Flags().Bool("local", false, localOnlyFlagHelp)
	}

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

	// Forensic read of user_sessions for an ARBITRARY user. No HTTP route
	// exists and none is proposed: an endpoint that enumerates any user's
	// session inventory is a new pre-auth-adjacent surface on the exact table
	// an attacker wants, and the audience for this command already has shell
	// access to the host. So it stays local — and refuses when the CLI names a
	// server, rather than reporting some other instance's sessions.
	db, err := openGatedLocalDB(cmd, "crewship admin sessions list", "")
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

	// When --active-only is set, push the predicate INTO the SQL
	// WHERE clause instead of filtering Go-side after the LIMIT.
	// The previous Go-side filter could return zero active rows
	// even when more existed deeper in the DB: imagine 100 sessions
	// where the newest 50 are revoked and the next 50 are active —
	// LIMIT 50 + Go-side filter sees zero actives. SQL-side
	// filtering means LIMIT counts actual matches.
	//
	// Boundary on expires_at matches classifyAdminSessionRow:
	// expires_at strictly greater than now (equality is "expired").
	whereClause := "WHERE user_id = ?"
	args := []any{userID}
	if activeOnly {
		whereClause += " AND revoked_at IS NULL AND datetime(expires_at) > datetime(?)"
		args = append(args, time.Now().UTC().Format(time.RFC3339))
	}
	args = append(args, limit)
	query := `
		SELECT id, created_at, expires_at, COALESCE(last_used_at, ''),
		       COALESCE(revoked_at, ''), COALESCE(revoked_reason, ''),
		       COALESCE(user_agent, ''), COALESCE(ip, '')
		FROM user_sessions
		` + whereClause + `
		ORDER BY created_at DESC
		LIMIT ?`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	sessions := []adminSessionRow{}
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
		sessions = append(sessions, adminSessionRow{
			ID:            id,
			Status:        status,
			CreatedAt:     createdAt,
			LastUsedAt:    lastUsedAt,
			ExpiresAt:     expiresAt,
			RevokedAt:     revokedAt,
			RevokedReason: revokedReason,
			IP:            ip,
			UserAgent:     userAgent,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sessions: %w", err)
	}

	displayName := fullName
	if displayName == "" {
		displayName = email
	}

	// This is a forensic read, so the machine rows carry the FULL session id,
	// the untruncated user agent, and revoked_at / revoked_reason as their own
	// fields. The human table cuts the id to 16 characters, the UA to 32, and
	// folds the reason into the status cell — all fine to read and none of it
	// safe to correlate against a server log.
	return resolvedFormatter(cmd).AutoHuman(adminSessionsResult{
		Email:       email,
		UserID:      userID,
		DisplayName: displayName,
		ActiveOnly:  activeOnly,
		Hidden:      hidden,
		Sessions:    sessions,
	}, func() {
		out := cmd.OutOrStdout()
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATUS\tCREATED\tLAST USED\tEXPIRES\tIP\tUA")
		for _, s := range sessions {
			statusCell := s.Status
			if s.Status == "revoked" && s.RevokedReason != "" {
				statusCell = "revoked:" + s.RevokedReason
			}
			dispID := s.ID
			if len(dispID) > 16 {
				dispID = dispID[:16]
			}
			dispIP := s.IP
			if dispIP == "" {
				dispIP = "-"
			}
			dispUA := s.UserAgent
			if dispUA == "" {
				dispUA = "-"
			}
			if len(dispUA) > 32 {
				dispUA = dispUA[:29] + "..."
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				dispID, statusCell, shortAdminTime(s.CreatedAt), shortAdminTime(s.LastUsedAt),
				shortAdminTime(s.ExpiresAt), dispIP, dispUA)
		}
		_ = tw.Flush()
		if len(sessions) == 0 {
			if activeOnly {
				fmt.Fprintf(out, "(no active sessions for %s)\n", displayName)
			} else {
				fmt.Fprintf(out, "(no sessions for %s)\n", displayName)
			}
		}
		if activeOnly && hidden > 0 {
			fmt.Fprintf(out, "\n(%d revoked/expired session(s) hidden by --active-only)\n", hidden)
		}
	})
}

// adminSessionRow is one user_sessions row in the forensic read.
type adminSessionRow struct {
	ID            string `json:"id"`
	Status        string `json:"status"` // active | revoked | expired
	CreatedAt     string `json:"created_at"`
	LastUsedAt    string `json:"last_used_at,omitempty"`
	ExpiresAt     string `json:"expires_at"`
	RevokedAt     string `json:"revoked_at,omitempty"`
	RevokedReason string `json:"revoked_reason,omitempty"`
	IP            string `json:"ip,omitempty"`
	UserAgent     string `json:"user_agent,omitempty"`
}

// adminSessionsResult is the machine-readable form of `admin sessions list`.
//
// `hidden` is carried because "0 rows" and "0 rows shown, 40 filtered out by
// --active-only" are different answers, and only the human output distinguished
// them.
type adminSessionsResult struct {
	Email       string            `json:"email"`
	UserID      string            `json:"user_id"`
	DisplayName string            `json:"display_name"`
	ActiveOnly  bool              `json:"active_only"`
	Hidden      int               `json:"hidden"`
	Sessions    []adminSessionRow `json:"sessions"`
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
		return t.UTC().Format("2006-01-02 15:04 UTC")
	}
	if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
		return t.UTC().Format("2006-01-02 15:04 UTC")
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

	// A write with no route. Revoking every session of an arbitrary user is
	// the incident-response twin of reset-password and belongs to the same
	// host-only family; the API's equivalent is the user logging themselves
	// out, which is not what an operator responding to a leak needs. Gated:
	// revoking sessions in the wrong database is not something a warning
	// makes safe.
	db, err := openGatedLocalDB(cmd, "crewship admin invalidate-sessions", "")
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
	// Filter on expires_at > now so the row count reported below
	// matches what `crewship admin sessions list --active-only`
	// considers active (revoked_at IS NULL AND expires_at > now).
	// Already-expired rows are unreachable to clients anyway, so
	// skipping them is purely a count/wording alignment, not a
	// security regression.
	res, err := db.ExecContext(ctx, `
		UPDATE user_sessions
		SET revoked_at = ?, revoked_reason = 'admin_invalidate'
		WHERE user_id = ?
		  AND revoked_at IS NULL
		  AND datetime(expires_at) > datetime(?)`, now, userID, now)
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

// openAdminDB is gone. It resolved ~/.crewship/crewship.db, claimed in its own
// comment to "mirror the resolution logic of the server", and never looked at
// --server / CREWSHIP_SERVER / --profile — so on every host where crewshipd
// runs with its own DATABASE_URL it opened a different database than the one
// it was being asked about, and said nothing (#2086). Its replacement is
// resolveLocalDBTarget + requireLocalDB in local_db_target.go, which names the
// file it picked and refuses when the operator has named a server.

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

	// The one command whose local-only nature is not a gap to close: routing
	// a password recovery through the server you are locked out of is
	// circular, which is why every comparable product ships exactly this.
	// Gated all the same — writing a new password hash into a database that
	// belongs to a different instance is a silent, irreversible wrong answer.
	db, err := openGatedLocalDB(cmd, "crewship admin reset-password", "")
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

	// api.ProductionBcryptCost, not a second copy of 12: this writes into
	// the same users.hashed_password column the signup path does, and
	// docs/guides/auth.mdx claims the cost is held constant across signup,
	// admin-CLI reset and pairing redemption. Until #2031 that was two
	// literals agreeing by coincidence.
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), api.ProductionBcryptCost)
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

// adminListUsersLocalHint is appended to every failure of the server-backed
// listing, because the command's oldest use — "who exists on this box, so I
// can reset the right password?" — is the one that has to survive a server
// that is down or a login that is the thing broken.
const adminListUsersLocalHint = "\n(if that server is down, or the login is what is broken, and you are on its host: " +
	"`crewship admin list-users --local` reads the database file directly)"

// adminAPIUser is one row of GET /api/v1/admin/users. Only the fields the
// table renders are decoded; the endpoint is workspace-scoped by middleware,
// so `workspace` is the caller's own and `role` is the membership in it.
type adminAPIUser struct {
	Email     string  `json:"email"`
	FullName  *string `json:"full_name"`
	CreatedAt string  `json:"created_at"`
	Workspace *struct {
		Slug string `json:"slug"`
	} `json:"workspace"`
	Role *string `json:"role"`
}

// runAdminListUsers reads the server the CLI targets, unless --local asks for
// the database file on this host.
//
// The two answers are genuinely different questions, and the help says so:
// the API is workspace-scoped (a workspace admin has no business enumerating
// every tenant on the instance), while the file is instance-wide and is the
// only place lockout state lives. What is NOT a difference is which one is
// authoritative about a server — before #2086 this command answered the
// server question from the file, and got it wrong wherever the two diverged.
func runAdminListUsers(cmd *cobra.Command, _ []string) error {
	if localOnlyFlag(cmd) {
		return runAdminListUsersLocal(cmd)
	}

	lockedOnly, _ := cmd.Flags().GetBool("locked-only")
	if lockedOnly {
		// Lockout state is not on the API. Filtering client-side on a field we
		// do not have would print "(no currently locked-out users)" for a
		// workspace full of them — the same silent-wrong-answer shape this
		// command is being fixed for.
		return cli.WithExitCode(errors.New(
			"--locked-only needs lockout state, which GET /api/v1/admin/users does not return.\n"+
				"Run it on the host that owns the database:  crewship admin list-users --locked-only --local"),
			cli.ExitValidation)
	}

	// Both failure paths carry the same hint. The original audience for this
	// command is an operator whose server is down or whose login is exactly
	// what is broken — "not logged in" or a bare connection error, with no
	// mention of the escape hatch, would be a worse answer than the one this
	// change removed.
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return fmt.Errorf("%w%s", err, adminListUsersLocalHint)
	}
	var users []adminAPIUser
	if err := getJSON(client, "/api/v1/admin/users", &users); err != nil {
		return fmt.Errorf("%w%s", err, adminListUsersLocalHint)
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tNAME\tCREATED\tROLE")
	for _, u := range users {
		name := "-"
		if u.FullName != nil && *u.FullName != "" {
			name = *u.FullName
		}
		role := "-"
		if u.Role != nil && *u.Role != "" {
			role = *u.Role
		}
		if u.Workspace != nil && u.Workspace.Slug != "" {
			role += "@" + u.Workspace.Slug
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Email, name, shortAdminTime(u.CreatedAt), role)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(users) == 0 {
		// stderr, for the same reason as the advisory below: this line is new
		// on this route, and on stdout it is the empty table's version of the
		// same break — `crewship admin list-users | awk 'NR>1 {print $1}'`
		// returns "(no" instead of nothing at all. An empty result is an empty
		// stdout plus a header; the explanation of WHY it is empty is prose.
		//
		// The `--local` branch's long-standing "(no users — run `crewship
		// seed` …)" stays on stdout: it is published example output in
		// docs/guides/admin-cli.mdx and moving it is a change to a documented
		// surface, not part of this fix.
		fmt.Fprintln(cmd.ErrOrStderr(), "(no users in this workspace)")
	}
	// Say what this view does NOT cover, so nobody reads a clean table as
	// "nobody is locked out".
	//
	// On stderr, like every other note this command family prints. Every
	// endpoint's CLI command is the contract agents drive, and prose in the
	// middle of stdout breaks it: on stdout this blank line and this sentence
	// came back from `crewship admin list-users | awk 'NR>1 {print $1}'` as if
	// they were two more user rows.
	fmt.Fprintln(cmd.ErrOrStderr(),
		"\n(workspace-scoped; lockout state lives on the database host — `crewship admin list-users --local`)")
	return nil
}

// runAdminListUsersLocal is the --local half: every user in the instance, plus
// the lockout columns, read from the database file on this host.
func runAdminListUsersLocal(cmd *cobra.Command) error {
	lockedOnly, _ := cmd.Flags().GetBool("locked-only")

	db, err := openGatedLocalDB(cmd, "crewship admin list-users --local", "")
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

	// The server-side equivalent exists — PATCH /api/v1/workspaces/{id}/
	// members/{memberId}, wrapped by `crewship workspace member role` — and it
	// is the right tool whenever you can log in. This one is not a duplicate
	// of it: the API path enforces the role ladder against the CALLER's role,
	// so it cannot mint the first OWNER, and it needs a session, which is what
	// the operator running this does not have. Local, and gated.
	db, err := openGatedLocalDB(cmd, "crewship admin promote",
		"crewship workspace member role <member-id|user-id> "+role)
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
