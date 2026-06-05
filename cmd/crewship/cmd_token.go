package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// emitToken writes a freshly-minted CLI token to the operator in the
// least-leaky way the flags allow. Both `token create` and
// `token rotate` route their bearer through here so the secret-safe
// paths stay identical.
//
// Precedence (most → least confined):
//
//	--output-file PATH → write the bare token to PATH with 0600 perms.
//	                     stdout gets only a confirmation line; the secret
//	                     never touches stdout / shell history / CI logs.
//	--quiet            → print ONLY the bare token (no labels), so
//	                     `TOKEN=$(crewship token create --quiet)` captures
//	                     cleanly. A one-line "sensitive" notice goes to
//	                     stderr (not captured by $(...)).
//	default            → the human-readable block, but the token line and
//	                     the "store this securely" warning go to STDERR
//	                     so a naive `... > file` or a CI step log doesn't
//	                     silently persist the bearer on stdout.
//
// The metadata (Name, ID) always goes to stdout in the default/file
// modes — only the token value itself is treated as the secret.
func emitToken(cmd *cobra.Command, name, id, token string) error {
	outFile, _ := cmd.Flags().GetString("output-file")
	quiet, _ := cmd.Flags().GetBool("quiet")
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	if outFile != "" {
		// 0600 + O_EXCL-free create: we overwrite an existing file the
		// operator pointed us at, but lock the perms so a rotated token
		// never lands world-readable. Trailing newline so the file is a
		// clean single-line secret for `cat`/`$(<file)` consumers.
		if err := os.WriteFile(outFile, []byte(token+"\n"), 0o600); err != nil {
			return fmt.Errorf("write token to %s: %w", outFile, err)
		}
		fmt.Fprintf(out, "%sToken created:%s %s\n", cli.Bold, cli.Reset, name)
		if id != "" {
			fmt.Fprintf(out, "%sID:%s     %s\n", cli.Dim, cli.Reset, id)
		}
		fmt.Fprintf(out, "%sToken written to %s (mode 0600).%s\n", cli.Dim, outFile, cli.Reset)
		return nil
	}

	if quiet {
		// Bare token on stdout for command-substitution capture; the
		// advisory rides stderr so it doesn't pollute the captured value.
		fmt.Fprintln(out, token)
		fmt.Fprintf(errOut, "%sToken is sensitive — it won't be shown again. Avoid shell history / CI logs.%s\n", cli.Yellow, cli.Reset)
		return nil
	}

	// Default human path. Metadata to stdout; the secret + warning to
	// stderr so a redirect of stdout doesn't quietly persist the bearer.
	fmt.Fprintf(out, "%sToken created:%s %s\n", cli.Bold, cli.Reset, name)
	if id != "" {
		fmt.Fprintf(out, "%sID:%s     %s\n", cli.Dim, cli.Reset, id)
	}
	fmt.Fprintf(errOut, "%sToken:%s  %s\n", cli.Dim, cli.Reset, token)
	fmt.Fprintf(errOut, "\n%sStore this token securely — it won't be shown again. (Use --output-file to write it straight to a 0600 file, or --quiet for capture.)%s\n", cli.Yellow, cli.Reset)
	return nil
}

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

		return emitToken(cmd, result.Name, result.ID, result.Token)
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
		if err := confirmAction(cmd, fmt.Sprintf("Revoke CLI token %q? Anything still using it starts getting 401 immediately.", args[0])); err != nil {
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

		// Emit the new credential BEFORE revoking the old one. If revoke
		// fails the user still has the new token and a clear instruction
		// to finish the rotation manually. emitToken honours
		// --output-file / --quiet so the rotated bearer can stay off
		// stdout / shell history just like create.
		if err := emitToken(cmd, created.Name, created.ID, created.Token); err != nil {
			return err
		}

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
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"valid": false}); err != nil {
					return fmt.Errorf("emit JSON: %w", err)
				}
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
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return fmt.Errorf("emit JSON: %w", err)
				}
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

	// Secret-safe output for the two commands that mint a bearer. Default
	// keeps the human block but routes the token value to stderr; these
	// flags add a 0600-file sink and a bare-stdout capture mode so the
	// token stays out of shell history / CI step logs.
	for _, c := range []*cobra.Command{tokenCreateCmd, tokenRotateCmd} {
		c.Flags().String("output-file", "", "Write the bare token to this file (mode 0600) instead of stdout")
		c.Flags().Bool("quiet", false, "Print only the bare token to stdout (for $(...) capture); advisory goes to stderr")
	}

	tokenListCmd.Flags().Int("warn-stale-days", 90, "Flag tokens older / unused longer than N days (0 disables)")
	tokenValidateCmd.Flags().Bool("json", false, "Emit machine-readable JSON to stdout instead of human-readable text")
	tokenRevokeCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	tokenCmd.AddCommand(tokenListCmd)
	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCmd.AddCommand(tokenRevokeCmd)
	tokenCmd.AddCommand(tokenRotateCmd)
	tokenCmd.AddCommand(tokenValidateCmd)
}
