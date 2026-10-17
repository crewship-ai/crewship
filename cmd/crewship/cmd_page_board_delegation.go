package main

// POST /api/v1/pages/{slug}/board/delegations page board delegate
// DELETE /api/v1/pages/{slug}/board/delegations/{runId} page board revoke

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newPageBoardDelegationCommand(revoke bool) *cobra.Command {
	verb, short := "delegate", "Authorize your live chat run to modify its agent's own Board drawings"
	if revoke {
		verb, short = "revoke", "Revoke a run's Board write delegation"
	}
	command := &cobra.Command{Use: verb + " <slug>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		run, _ := cmd.Flags().GetString("run")
		if strings.TrimSpace(run) == "" {
			return fmt.Errorf("--run is required")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		if revoke {
			response, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/board/delegations/" + pagePathEscape(run))
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := pageCheckError(response); err != nil {
				return err
			}
			return resolvedFormatter(cmd).AutoHuman(map[string]any{"run_id": run, "revoked": true}, func() { fmt.Fprintln(cmd.OutOrStdout(), "Board run delegation revoked") })
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/board/delegations", map[string]string{"run_id": run})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	command.Flags().String("run", "", "Required live human-initiated chat run; the agent also needs a Page write grant")
	return command
}
