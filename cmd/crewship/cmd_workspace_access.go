package main

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func init() {
	command := &cobra.Command{Use: "access", Short: "Inspect your own workspace access"}
	command.AddCommand(&cobra.Command{
		Use: "me", Short: "Explain your current grants and execution prerequisites", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireAuth(); err != nil {
				return err
			}
			if err := requireWorkspace(); err != nil {
				return err
			}
			client := newAPIClient()
			response, err := client.Get("/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + "/access/me")
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err = cli.CheckError(response); err != nil {
				return err
			}
			var result memberEffectiveExplanation
			if err = cli.ReadJSON(response, &result); err != nil {
				return err
			}
			rows := [][]string{}
			for _, decision := range result.Decisions {
				rows = append(rows, []string{decision.Kind, decision.resourceName(), decision.Operation, decision.State, decision.Reason})
			}
			if len(rows) == 0 {
				rows = append(rows, []string{"", "", "", "", result.Explanation})
			}
			return newFormatter().Auto(result, []string{"KIND", "RESOURCE", "OPERATION", "STATE", "REASON"}, rows)
		},
	})
	workspaceCmd.AddCommand(command)
}
