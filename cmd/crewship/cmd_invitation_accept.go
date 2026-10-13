package main

import (
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func init() {
	workspaceInviteCmd.AddCommand(&cobra.Command{
		Use: "accept <token>", Short: "Accept an invitation with the signed-in account", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireAuth(); err != nil {
				return err
			}
			response, err := newAPIClient().Post("/api/v1/auth/invitation/accept", map[string]string{"token": args[0]})
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
	})
}
