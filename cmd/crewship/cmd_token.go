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
	tokenCmd.AddCommand(tokenListCmd)
	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCmd.AddCommand(tokenRevokeCmd)
	tokenCmd.AddCommand(tokenValidateCmd)
}
