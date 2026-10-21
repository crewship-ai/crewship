package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newIssueDeliveriesCommand() *cobra.Command {
	group := &cobra.Command{Use: "deliveries", Short: "Inspect issue mutation delivery diagnostics"}
	failed := &cobra.Command{Use: "failed", Short: "List sanitized failed delivery jobs (workspace administrators only)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be between 1 and 100")
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		q := url.Values{"state": {"failed"}, "limit": {fmt.Sprint(limit)}}
		cursor, _ := cmd.Flags().GetString("cursor")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page map[string]any
		if err = getJSON(client, "/api/v1/workspaces/"+url.PathEscape(client.GetWorkspaceID())+"/issue-mutation-outbox?"+q.Encode(), &page); err != nil {
			return err
		}
		if filter, _ := cmd.Flags().GetString("filter"); filter != "" {
			return emitJSONFiltered(cmd, page)
		}
		return resolvedFormatter(cmd).Machine(page)
	}}
	failed.Flags().Int("limit", 50, "Maximum failed jobs per page (1–100)")
	failed.Flags().String("cursor", "", "Opaque next_cursor from the preceding page")
	group.AddCommand(failed)
	return group
}
func init() { issueCmd.AddCommand(newIssueDeliveriesCommand()) }
