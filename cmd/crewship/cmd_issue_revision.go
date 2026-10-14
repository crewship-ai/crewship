package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newIssueRevisionCommand() *cobra.Command {
	return &cobra.Command{Use: "revision <identifier-or-id>", Short: "Read current issue field and aggregate revision counters", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateEntityCLIIdentifier(args[0]); err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		// An explicit workspace selection uses the workspace resource path; a
		// configured selection uses the canonical issue alias. Both authorize the
		// same selected workspace and resolve an identifier or an internal ID.
		path := "/api/v1/issues/" + url.PathEscape(args[0]) + "/revision"
		if cmd.Flags().Changed("workspace") {
			path = fmt.Sprintf("/api/v1/workspaces/%s/issues/%s/revision", url.PathEscape(client.GetWorkspaceID()), url.PathEscape(args[0]))
		}
		var result map[string]any
		if err = getJSON(client, path, &result); err != nil {
			return err
		}
		if filter, _ := cmd.Flags().GetString("filter"); filter != "" {
			return emitJSONFiltered(cmd, result)
		}
		return resolvedFormatter(cmd).Machine(result)
	}}
}
func init() { issueCmd.AddCommand(newIssueRevisionCommand()) }
