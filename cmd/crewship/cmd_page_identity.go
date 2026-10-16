package main

import "github.com/spf13/cobra"

func newPageResolveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <page-id>",
		Short: "Resolve a stable Page identity to its current authorized address",
		Long:  "Resolve a Page ID within the selected workspace. Requires current human read access and pages:read token scope; hidden and missing identities share the same404 response. Returns only page_id and current slug.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := pageClient()
			if err != nil {
				return err
			}
			response, err := client.Get("/api/v1/pages/resolve/id/" + pagePathEscape(args[0]))
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		},
	}
}

func init() { pageCmd.AddCommand(newPageResolveCommand()) }
