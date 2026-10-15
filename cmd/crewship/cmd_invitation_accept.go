package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func init() {
	var fromStdin, confirmGuest bool
	command := &cobra.Command{
		Use: "accept --token-stdin", Short: "Accept an invitation with proof read from standard input", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireAuth(); err != nil {
				return err
			}
			if !fromStdin {
				return fmt.Errorf("read invitation proof with --token-stdin; do not put it in command arguments")
			}
			input, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8193))
			if err != nil {
				return fmt.Errorf("read invitation proof: %w", err)
			}
			token := strings.TrimSpace(string(input))
			if len(input) > 8192 || token == "" {
				return fmt.Errorf("invitation proof must be non-empty and at most 8192 bytes")
			}
			response, err := newAPIClient().Post("/api/v1/auth/invitation/accept", map[string]any{"token": token, "confirm_account_restriction": confirmGuest})
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := cli.CheckError(response); err != nil {
				return err
			}
			var result struct {
				OK bool `json:"ok" yaml:"ok"`
			}
			if err := cli.ReadJSON(response, &result); err != nil {
				return err
			}
			return resolvedFormatter(cmd).AutoHuman(result, func() { cli.PrintSuccess("Invitation accepted. Refresh your workspace list to select it.") })
		},
	}
	command.Flags().BoolVar(&fromStdin, "token-stdin", false, "Read invitation proof from standard input (avoids shell history and process arguments)")
	command.Flags().BoolVar(&confirmGuest, "confirm-account-restriction", false, "Explicitly consent to Guest restrictions across all workspaces; OWNER/ADMIN accounts are refused")
	workspaceInviteCmd.AddCommand(command)
}
