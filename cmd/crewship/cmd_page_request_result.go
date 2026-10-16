package main

// GET /api/v1/page-request-results/{kind}/{requestId} page request-result

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	pageCmd.AddCommand(&cobra.Command{
		Use:   "request-result <artifact|board> <request-id>",
		Short: "Resolve your request result using current Page access",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "artifact" && args[0] != "board" {
				return fmt.Errorf("kind must be artifact or board")
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			response, err := client.Get("/api/v1/page-request-results/" + args[0] + "/" + pagePathEscape(args[1]))
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		},
	})
}
