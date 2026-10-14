package main

// GET /api/v1/workspaces/{workspaceId}/page-ownership workspace page-ownership list
// POST /api/v1/workspaces/{workspaceId}/page-ownership/{pageId} workspace page-ownership transfer

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func init() { workspaceCmd.AddCommand(newWorkspacePageOwnershipCommand()) }

func newWorkspacePageOwnershipCommand() *cobra.Command {
	command := &cobra.Command{Use: "page-ownership", Short: "Review and explicitly transfer human-owned Artifacts and Boards"}
	list := &cobra.Command{Use: "list", Short: "List output owners and eligible successors with current read access", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		owner, _ := cmd.Flags().GetString("owner")
		limit, _ := cmd.Flags().GetInt("limit")
		cursor, _ := cmd.Flags().GetString("cursor")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be between 1 and 100")
		}
		if cmd.Flags().Changed("cursor") && !validPageOwnershipID(cursor) {
			return fmt.Errorf("--cursor must be a nonempty token returned by list")
		}
		if cmd.Flags().Changed("owner") && !validPageOwnershipID(owner) {
			return fmt.Errorf("--owner must be a nonempty user ID without surrounding whitespace")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		params := url.Values{"limit": {strconv.Itoa(limit)}}
		if owner != "" {
			params.Set("owner_user_id", owner)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		endpoint := "/api/v1/workspaces/" + pagePathEscape(client.WorkspaceID) + "/page-ownership?" + params.Encode()
		response, err := client.Get(endpoint)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	list.Flags().String("owner", "", "Filter by the current owner's user ID")
	list.Flags().Int("limit", 25, "Maximum outputs per response (1–100); follow next_cursor for more")
	list.Flags().String("cursor", "", "Opaque next_cursor from the previous list response; keep the same owner filter")
	transfer := &cobra.Command{Use: "transfer <page-id>", Short: "Immediately transfer a private output and its history to an existing reader", Long: "Immediately transfer an Artifact or Board to an eligible human who already has current read access. The new owner gains control of the output and its retained history; content and history stay with the Page, existing grants are rechecked under current permissions, and the new owner may need to review sharing. Linked sources keep separate access checks. Review page-ownership list first, transfer every affected output explicitly, then retry the departure.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		expected, _ := cmd.Flags().GetString("expected-owner")
		successor, _ := cmd.Flags().GetString("successor")
		if !validPageOwnershipID(args[0]) || !validPageOwnershipID(expected) || !validPageOwnershipID(successor) {
			return fmt.Errorf("page ID, --expected-owner and --successor are required nonempty IDs without surrounding whitespace")
		}
		if expected == successor {
			return fmt.Errorf("--successor must differ from --expected-owner")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/workspaces/"+pagePathEscape(client.WorkspaceID)+"/page-ownership/"+pagePathEscape(args[0]), map[string]any{"expected_owner_user_id": expected, "successor_user_id": successor})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	transfer.Flags().String("expected-owner", "", "Reviewed current owner user ID; a changed owner causes a conflict")
	transfer.Flags().String("successor", "", "Eligible successor user ID returned by list")
	command.AddCommand(list, transfer)
	return command
}

func validPageOwnershipID(value string) bool { return value != "" && strings.TrimSpace(value) == value }
