package main

// POST   /api/v1/pages/{slug}/artifact/delegations         page artifact delegate
// DELETE /api/v1/pages/{slug}/artifact/delegations/{runId} page artifact revoke

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newPageArtifactDelegationCommand(revoke bool) *cobra.Command {
	verb, short := "delegate", "Authorize your live human-initiated chat run to update this artifact"
	if revoke {
		verb, short = "revoke", "Revoke an artifact publication delegation for a run"
	}
	cmd := &cobra.Command{Use: verb + " <slug>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		run, _ := cmd.Flags().GetString("run")
		if strings.TrimSpace(run) == "" {
			return fmt.Errorf("--run is required")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		if revoke {
			response, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/delegations/" + pagePathEscape(run))
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := pageCheckError(response); err != nil {
				return err
			}
			return resolvedFormatter(cmd).AutoHuman(map[string]any{"run_id": run, "revoked": true}, func() { fmt.Fprintln(cmd.OutOrStdout(), "Artifact run delegation revoked") })
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/delegations", map[string]any{"run_id": run})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().String("run", "", "Required live chat run ID; delegation does not create agent access grants")
	return cmd
}
