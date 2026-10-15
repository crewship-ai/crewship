package main

// GET /api/v1/pages/{slug}/artifact/lease page artifact lease

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPageArtifactLeaseCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "lease <slug>", Args: cobra.ExactArgs(1), Short: "Check current access to exact artifact revision metadata", RunE: func(cmd *cobra.Command, args []string) error {
		revision, _ := cmd.Flags().GetInt64("revision")
		if revision < 1 {
			return fmt.Errorf("--revision must be positive")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get(fmt.Sprintf("/api/v1/pages/%s/artifact/lease?revision=%d", pagePathEscape(args[0]), revision))
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().Int64("revision", 0, "Required exact retained revision; no source content is read")
	return cmd
}
