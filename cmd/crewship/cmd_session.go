package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// sessionCmd surfaces the active-sessions API the web UI's
// Settings → Sessions panel uses (GET /api/v1/auth/sessions and
// POST /api/v1/auth/sessions/{id}/revoke). Mirroring it on the CLI
// matters for two flows operators routinely script:
//
//  1. "Audit who's logged in" — sessions list shows the device, IP, and
//     when each session was last seen. Pipe through `jq` for compliance
//     reports without opening a browser.
//  2. "Force logout" — sessions revoke kills a session by ID. Combined
//     with `whoami` and `token revoke` it gives an admin everything
//     needed to neutralise a leaked credential.
//
// Revoking the caller's own session is allowed by design; the server
// returns is_current=true so a careless script can warn the human before
// it locks itself out.
var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "List and revoke active browser sessions",
	Long: `Manage the caller's active browser sessions — the same surface the
Settings → Sessions web panel exposes.

Examples:
  crewship session list
  crewship session list --format json | jq
  crewship session revoke <session-id>`,
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions for the current user",
	Long: `List every active browser session for the caller.

Sessions older than --warn-stale-days (default 30) are flagged in the
STATUS column as "stale". Stale sessions belong to devices the user
hasn't logged in from recently — a laptop locked at the cafe, a
browser on a borrowed machine, an old phone. A summary at the bottom
suggests revoking when any stale sessions are found.

The default threshold mirrors the industry norm for browser
session-cookie expiry (30 days). Pass 0 to disable the check.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		warnStaleDays, _ := cmd.Flags().GetInt("warn-stale-days")

		client := newAPIClient()
		// Sessions are user-scoped, not workspace-scoped; clear the
		// workspace_id query param the default client injects so the
		// request lands clean.
		client.WorkspaceID = ""

		resp, err := client.Get("/api/v1/auth/sessions")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var sessions []struct {
			ID         string `json:"id"`
			CreatedAt  string `json:"created_at"`
			LastUsedAt string `json:"last_used_at"`
			UserAgent  string `json:"user_agent"`
			IP         string `json:"ip"`
			IsCurrent  bool   `json:"is_current"`
		}
		if err := cli.ReadJSON(resp, &sessions); err != nil {
			return err
		}

		now := time.Now().UTC()
		var staleIDs []string
		f := newFormatter()
		headers := []string{"ID", "STATUS", "CREATED", "LAST USED", "IP", "USER AGENT"}
		rows := make([][]string, 0, len(sessions))
		for _, s := range sessions {
			created := s.CreatedAt
			if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
				created = t.Format("2006-01-02 15:04")
			}
			lastUsed := s.LastUsedAt
			if t, err := time.Parse(time.RFC3339, s.LastUsedAt); err == nil {
				lastUsed = t.Format("2006-01-02 15:04")
			}
			status := classifySessionStatus(s.IsCurrent, s.LastUsedAt, warnStaleDays, now)
			if status == "stale" {
				staleIDs = append(staleIDs, s.ID)
			}
			ua := truncateString(s.UserAgent, 32)
			ip := s.IP
			if ip == "" {
				ip = "-"
			}
			rows = append(rows, []string{truncateString(s.ID, 16), status, created, lastUsed, ip, ua})
		}
		if err := f.Auto(sessions, headers, rows); err != nil {
			return err
		}
		// Table-mode footer suggests cleanup. Suppressed in JSON output
		// because consumers parse STATUS programmatically.
		if f.Format == "table" && len(staleIDs) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\n%s%d stale session(s) found.%s Revoke with: crewship session revoke <id>\n",
				cli.Yellow, len(staleIDs), cli.Reset)
		}
		return nil
	},
}

// classifySessionStatus returns one of: current, active, stale.
//
//   - "current" → the caller's own session right now (is_current=true wins)
//   - "active"  → last_used_at within warnStaleDays
//   - "stale"   → last_used_at older than warnStaleDays
//
// warnStaleDays <= 0 disables the stale check entirely (every non-
// current session is "active"). Negative values clamp to 0 so a flag
// typo can't pressure the user to revoke every healthy session.
//
// Parse failures on the timestamp default to "active" rather than
// "stale" — a malformed last_used_at is a server-side bug, and
// flagging healthy sessions because of it would be worse than the
// conservative miss. The is_current flag also short-circuits the
// stale check: a session you're literally using right now MUST NOT
// be marked stale even if its last_used_at hasn't been updated this
// request cycle.
func classifySessionStatus(isCurrent bool, lastUsedAt string, warnStaleDays int, now time.Time) string {
	if isCurrent {
		return "current"
	}
	if warnStaleDays <= 0 {
		return "active"
	}
	t, err := time.Parse(time.RFC3339, lastUsedAt)
	if err != nil {
		return "active"
	}
	threshold := time.Duration(warnStaleDays) * 24 * time.Hour
	if now.Sub(t) > threshold {
		return "stale"
	}
	return "active"
}

var sessionRevokeCmd = &cobra.Command{
	Use:   "revoke <session-id>",
	Short: "Revoke a session by ID",
	Long: `Revoke a single session. The session id must belong to the caller;
foreign ids return 404 (the same shape as "doesn't exist") so callers
can't enumerate other users' sessions by guessing.

If you revoke your own current session, the next CLI/web request will
return 401 — you'll need to log in again.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		client.WorkspaceID = ""

		path := "/api/v1/auth/sessions/" + url.PathEscape(args[0]) + "/revoke"
		resp, err := client.Post(path, nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			OK        bool   `json:"ok"`
			ID        string `json:"id"`
			IsCurrent bool   `json:"is_current"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Session %s revoked.", out.ID))
		if out.IsCurrent {
			// Warn separately rather than failing — the user explicitly
			// asked for this, but they may not realise the consequence.
			fmt.Printf("%sNote: that was your current session — re-run 'crewship login' to continue.%s\n",
				cli.Yellow, cli.Reset)
		}
		return nil
	},
}

func init() {
	sessionListCmd.Flags().Int("warn-stale-days", 30, "Flag sessions whose last_used_at is older than N days (0 disables)")

	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionRevokeCmd)
}
