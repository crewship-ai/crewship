//go:build !clionly

package main

import (
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/spf13/cobra"
)

func newAdminCleanupCmd() *cobra.Command {
	return &cobra.Command{Use: "cleanup", Short: "Read container cleanup observations from the selected server (admin)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		response, err := newAPIClient().Get("/api/v1/admin/resource-cleanup")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := cli.CheckError(response); err != nil {
			return err
		}
		var body struct {
			Scope string                     `json:"scope" yaml:"scope"`
			State string                     `json:"state" yaml:"state"`
			Items []resourcelifecycle.Status `json:"items" yaml:"items"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(body, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Container cleanup: %s\n", body.State)
			for _, s := range body.Items {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s remaining=%d unattributed=%d error=%s\n", s.CrewID, s.State, s.Remaining, s.Unattributed, s.Error)
			}
		})
	}}
}
func init() { adminCmd.AddCommand(newAdminCleanupCmd()) }
