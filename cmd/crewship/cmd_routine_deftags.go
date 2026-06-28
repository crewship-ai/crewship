package main

import (
	"fmt"
	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Routine-definition tags (v123) — tag a routine for cross-crew
// discovery (`routine list --tag billing`). Distinct from run tags.

var routineTagCmd = &cobra.Command{
	Use:   "tag <slug>",
	Short: "Add or remove discovery tags on a routine definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		add, _ := cmd.Flags().GetStringSlice("add")
		remove, _ := cmd.Flags().GetString("remove")
		if len(add) == 0 && remove == "" {
			return fmt.Errorf("pass --add <tag> (repeatable) and/or --remove <tag>")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		if len(add) > 0 {
			resp, err := client.Do("PUT",
				fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/tags", ws, args[0]),
				map[string]any{"tags": add})
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			fmt.Printf("Tagged %s: +%v\n", args[0], add)
		}
		if remove != "" {
			resp, err := client.Do("DELETE",
				fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/tags/%s", ws, args[0], remove),
				http.NoBody)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			fmt.Printf("Untagged %s: -%s\n", args[0], remove)
		}
		return nil
	},
}

func init() {
	routineTagCmd.Flags().StringSlice("add", nil, "tag(s) to add (repeatable)")
	routineTagCmd.Flags().String("remove", "", "tag to remove")
	pipelineCmd.AddCommand(routineTagCmd)
}
