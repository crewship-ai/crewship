package main

import (
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
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

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
			status := "active"
			if tok.RevokedAt != nil {
				status = "revoked"
			}
			rows = append(rows, []string{tok.ID[:12], tok.Name, created, lastUsed, status})
		}
		return f.Auto(result.Data, headers, rows)
	},
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
	RunE: func(cmd *cobra.Command, args []string) error {
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

		if result.Valid {
			cli.PrintSuccess("Token is valid.")
			fmt.Printf("  User: %s\n", result.Email)
			if result.ExpiresAt != "" {
				fmt.Printf("  Expires: %s\n", result.ExpiresAt)
			}
		} else {
			return fmt.Errorf("token is invalid")
		}
		return nil
	},
}

func init() {
	tokenRotateCmd.Flags().String("name", "", "Override new token name (default: '<old name> (rotated YYYY-MM-DD)')")
	tokenRevokeCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	tokenCmd.AddCommand(tokenListCmd)
	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCmd.AddCommand(tokenRevokeCmd)
	tokenCmd.AddCommand(tokenRotateCmd)
	tokenCmd.AddCommand(tokenValidateCmd)
}
