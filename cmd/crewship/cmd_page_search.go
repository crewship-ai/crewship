package main

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

func newPageSearchCommand() *cobra.Command {
	var cursor string
	var limit int
	command := &cobra.Command{
		Use:   "search <query>",
		Short: "Search current native Docs/Slides text in the selected workspace",
		Long:  "Search current supported native Docs/Slides plain text under current human Page read access and pages:read scope. Terms are literal folded AND terms. Incomplete coverage returns an error, not partial matches. Results are ordered by stable Page ID. Use the returned opaque cursor explicitly; no automatic pagination or retry.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 || limit > 50 {
				return fmt.Errorf("limit must be between 1 and 50")
			}
			if cmd.Flags().Changed("cursor") && cursor == "" {
				return fmt.Errorf("cursor must not be empty")
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			query := url.Values{"q": {args[0]}, "limit": {strconv.Itoa(limit)}}
			if cursor != "" {
				query.Set("cursor", cursor)
			}
			response, err := client.Get("/api/v1/pages/search/content?" + query.Encode())
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		},
	}
	command.Flags().StringVar(&cursor, "cursor", "", "Opaque continuation from this credential/workspace/query; live results, expires after five minutes")
	command.Flags().IntVar(&limit, "limit", 50, "Maximum visible Page matches (1–50)")
	return command
}
func init() { pageCmd.AddCommand(newPageSearchCommand()) }
