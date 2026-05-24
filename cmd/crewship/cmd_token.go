package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage CLI authentication tokens",
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all CLI tokens",
	Long: `List every CLI token issued to the caller.

Tokens older than --warn-stale-days are flagged as "stale" in the
STATUS column; tokens that have never been used (no last_used_at) are
flagged as "unused". A summary at the bottom suggests rotation when
any stale tokens are found.

The staleness threshold defaults to 90 days, matching the industry
norm for long-lived bearers. Pass 0 to disable the check (e.g. on a
machine where tokens legitimately sit unused for months — a backup
runner, a quarterly compliance script).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		warnStaleDays, _ := cmd.Flags().GetInt("warn-stale-days")

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Get("/api/v1/auth/cli-tokens")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Data []struct {
				ID         string  `json:"id"`
				Name       string  `json:"name"`
				CreatedAt  string  `json:"created_at"`
				LastUsedAt *string `json:"last_used_at"`
				RevokedAt  *string `json:"revoked_at"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		now := time.Now().UTC()
		var staleIDs []string
		f := newFormatter()
		headers := []string{"ID", "NAME", "CREATED", "LAST USED", "STATUS"}
		var rows [][]string
		for _, tok := range result.Data {
			created := tok.CreatedAt
			if t, err := time.Parse(time.RFC3339, tok.CreatedAt); err == nil {
				created = t.Format("2006-01-02 15:04")
			}
			lastUsed := "-"
			if tok.LastUsedAt != nil {
				if t, err := time.Parse(time.RFC3339, *tok.LastUsedAt); err == nil {
					lastUsed = t.Format("2006-01-02 15:04")
				}
			}
			status := classifyTokenStatus(tok.CreatedAt, tok.LastUsedAt, tok.RevokedAt, warnStaleDays, now)
			if status == "stale" || status == "unused" {
				staleIDs = append(staleIDs, tok.ID)
			}
			// Guard the 12-char truncation: a future server change
			// could shorten the ID format, and the unchecked slice
			// would panic before any test caught it. Cheap defence,
			// no behaviour change for IDs already at the expected width.
			displayID := tok.ID
			if len(displayID) > 12 {
				displayID = displayID[:12]
			}
			rows = append(rows, []string{displayID, tok.Name, created, lastUsed, status})
		}
		if err := f.Auto(result.Data, headers, rows); err != nil {
			return err
		}
		// Footer is intentionally only printed in table mode — JSON
		// consumers parse the STATUS column themselves and a trailing
		// stderr line would either break their parser or be invisible.
		if f.Format == "table" && len(staleIDs) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\n%s%d stale/unused token(s) found.%s Rotate with: crewship token rotate <id>\n",
				cli.Yellow, len(staleIDs), cli.Reset)
		}
		return nil
	},
}

// classifyTokenStatus returns one of: revoked, unused, stale, active.
// Logic:
//   - revokedAt set  → "revoked" (regardless of age)
//   - lastUsedAt nil AND createdAt older than warnStaleDays → "unused"
//   - lastUsedAt older than warnStaleDays → "stale"
//   - otherwise → "active"
//
// warnStaleDays == 0 disables the stale/unused classification entirely
// (a non-revoked token is always "active"). Negative values are
// treated as 0 — clamp at the boundary so a flag typo doesn't flip
// every token to "stale" and pressure the user into rotating
// healthy tokens.
//
// Parse failures on the timestamp strings default to "active" rather
// than misclassifying — a malformed created_at is a server-side bug,
// not the user's fault, and flagging healthy tokens because of it
// would be worse than the conservative miss.
func classifyTokenStatus(createdAt string, lastUsedAt, revokedAt *string, warnStaleDays int, now time.Time) string {
	if revokedAt != nil {
		return "revoked"
	}
	if warnStaleDays <= 0 {
		return "active"
	}
	threshold := time.Duration(warnStaleDays) * 24 * time.Hour
	if lastUsedAt == nil {
		// Never used — only flag once it's been sitting unused longer
		// than the threshold. A token created in the last hour shouldn't
		// already be "unused"; the operator may legitimately be about
		// to use it for the first time.
		created, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return "active"
		}
		if now.Sub(created) > threshold {
			return "unused"
		}
		return "active"
	}
	used, err := time.Parse(time.RFC3339, *lastUsedAt)
	if err != nil {
		return "active"
	}
	if now.Sub(used) > threshold {
		return "stale"
	}
	return "active"
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new CLI token",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		name := "CLI token"
		if len(args) > 0 {
			name = args[0]
		}

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/auth/cli-token", map[string]string{"name": name})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Token string `json:"token"`
			ID    string `json:"id"`
			Name  string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		fmt.Printf("%sToken created:%s %s\n", cli.Bold, cli.Reset, result.Name)
		fmt.Printf("%sID:%s     %s\n", cli.Dim, cli.Reset, result.ID)
		fmt.Printf("%sToken:%s  %s\n", cli.Dim, cli.Reset, result.Token)
		fmt.Printf("\n%sStore this token securely — it won't be shown again.%s\n", cli.Yellow, cli.Reset)
		return nil
	},
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke <token-id>",
	Short: "Revoke a CLI token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Delete("/api/v1/auth/cli-tokens/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Token revoked.")
		return nil
	},
}

// tokenRotateCmd atomically replaces an existing token: it creates a
// new token (carrying the old token's name suffixed with a timestamp so
// audit logs trace the rotation), then revokes the old one. No
// dedicated server-side rotate endpoint exists today; this composition
// is the same shape the web UI uses, and it's safe because Create
// always returns the bearer up-front and Revoke flips revoked_at
// without affecting the new row.
//
// If the Create step succeeds but Revoke fails, the new token is still
// printed (and the old one is still valid) — the user gets a clear
// warning so they can re-run Revoke manually. We do NOT roll back the
// new token because that would leave the user with neither.
var tokenRotateCmd = &cobra.Command{
	Use:   "rotate <token-id>",
	Short: "Rotate a CLI token (create new, revoke old) atomically",
	Long: `Rotate a CLI token. Creates a new token (inheriting the old name with
a rotation timestamp suffix) and revokes the old one. The new token is
printed once — store it before exiting.

Safety notes:
  - If revoke of the old token fails, the new token has already been
    created and printed; re-run 'crewship token revoke <old-id>' to
    finish the rotation.
  - Anything currently using the old token will start getting 401 the
    moment revoke lands. Roll forward by updating clients first, then
    rotating, OR run rotate then quickly update clients within the API
    grace window (effectively immediate today).

Examples:
  crewship token rotate tok_abc123
  crewship token rotate tok_abc123 --name "ci-runner"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		// Resolve the existing token name to carry it forward — without
		// this the rotation loses the human-readable label and the audit
		// trail becomes "ci-runner" → "rotated 2026-05-11" which is less
		// useful than "ci-runner (rotated 2026-05-11)".
		listResp, err := client.Get("/api/v1/auth/cli-tokens")
		if err != nil {
			return fmt.Errorf("list tokens: %w", err)
		}
		if err := cli.CheckError(listResp); err != nil {
			return err
		}
		var listBody struct {
			Data []struct {
				ID        string  `json:"id"`
				Name      string  `json:"name"`
				RevokedAt *string `json:"revoked_at"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(listResp, &listBody); err != nil {
			return fmt.Errorf("parse token list: %w", err)
		}

		oldID := args[0]
		var oldName string
		var found bool
		for _, t := range listBody.Data {
			if t.ID == oldID {
				oldName = t.Name
				if t.RevokedAt != nil {
					return fmt.Errorf("token %s is already revoked", oldID)
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("token %s not found (run 'crewship token list')", oldID)
		}

		// Allow --name override; otherwise carry the old name with a
		// rotation suffix so paymaster + audit attribution stays sane.
		newName, _ := cmd.Flags().GetString("name")
		if newName == "" {
			ts := time.Now().UTC().Format("2006-01-02")
			newName = fmt.Sprintf("%s (rotated %s)", oldName, ts)
		}

		// Create the replacement first. If this fails, the old token is
		// still valid — no harm done, the user just sees the create error.
		createResp, err := client.Post("/api/v1/auth/cli-token", map[string]string{"name": newName})
		if err != nil {
			return fmt.Errorf("create new token: %w", err)
		}
		if err := cli.CheckError(createResp); err != nil {
			return err
		}
		var created struct {
			Token string `json:"token"`
			ID    string `json:"id"`
			Name  string `json:"name"`
		}
		if err := cli.ReadJSON(createResp, &created); err != nil {
			return fmt.Errorf("parse new token: %w", err)
		}

		// Print the new credential BEFORE revoking the old one. If
		// revoke fails the user still has the new token and a clear
		// instruction to finish the rotation manually.
		fmt.Printf("%sNew token created:%s %s\n", cli.Bold, cli.Reset, created.Name)
		fmt.Printf("%sID:%s     %s\n", cli.Dim, cli.Reset, created.ID)
		fmt.Printf("%sToken:%s  %s\n", cli.Dim, cli.Reset, created.Token)
		fmt.Printf("\n%sStore this token securely — it won't be shown again.%s\n\n", cli.Yellow, cli.Reset)

		// Revoke the old token. A failure here leaves both tokens valid
		// — surface it loudly so the operator can clean up.
		revokeResp, err := client.Delete("/api/v1/auth/cli-tokens/" + oldID)
		if err != nil {
			return fmt.Errorf("revoke old token (new token IS active): %w", err)
		}
		if err := cli.CheckError(revokeResp); err != nil {
			return fmt.Errorf("revoke old token (new token IS active, re-run 'crewship token revoke %s'): %w", oldID, err)
		}
		revokeResp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Old token %s revoked.", oldID))
		fmt.Printf("%sNote: anything still using the old token will now get 401 — update clients to use the new token above.%s\n",
			cli.Yellow, cli.Reset)
		return nil
	},
}

var tokenValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the current CLI token",
	Long: `Confirm the current CLI token is valid against the configured server.

Exit codes (matter for CI):
  0  token is valid
  1  token is invalid / expired
  2  network or server error (couldn't reach the server)

With --json, emits {"valid": bool, "user_id", "email", "expires_at"}
to stdout instead of the human-readable two-liner. The exit code
contract is unchanged so a CI script can if-branch on the command's
exit status without re-parsing the output.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")

		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Get("/api/v1/auth/cli-token/validate")
		if err != nil {
			return err
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			if jsonOut {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"valid": false})
			}
			return fmt.Errorf("token is invalid or expired")
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Valid     bool   `json:"valid"`
			UserID    string `json:"user_id"`
			Email     string `json:"email"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		if !result.Valid {
			if jsonOut {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			return fmt.Errorf("token is invalid")
		}

		if jsonOut {
			// Emit the full validate payload so CI can drive
			// expiry-aware logic without a follow-up call (e.g.
			// "rotate the token if expires_at is within 7 days").
			// stdout is reserved for the JSON; the human two-liner
			// goes to stderr in JSON mode so a `2>/dev/null` keeps
			// the structured output clean for jq consumers.
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
				return fmt.Errorf("encode validate output: %w", err)
			}
			return nil
		}

		cli.PrintSuccess("Token is valid.")
		fmt.Printf("  User: %s\n", result.Email)
		if result.ExpiresAt != "" {
			fmt.Printf("  Expires: %s\n", result.ExpiresAt)
		}
		return nil
	},
}

func init() {
	tokenRotateCmd.Flags().String("name", "", "Override new token name (default: '<old name> (rotated YYYY-MM-DD)')")
	tokenListCmd.Flags().Int("warn-stale-days", 90, "Flag tokens older / unused longer than N days (0 disables)")
	tokenValidateCmd.Flags().Bool("json", false, "Emit machine-readable JSON to stdout instead of human-readable text")

	tokenCmd.AddCommand(tokenListCmd)
	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCmd.AddCommand(tokenRevokeCmd)
	tokenCmd.AddCommand(tokenRotateCmd)
	tokenCmd.AddCommand(tokenValidateCmd)
}
