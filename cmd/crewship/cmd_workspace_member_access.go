package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/cli"
)

var workspaceMemberAccessCmd = &cobra.Command{
	Use: "access", Short: "Read or replace a member's resource access policy",
	Long: `Manage a member's versioned resource policy (trusted workspace admin only).
Restricted mode denies unintegrated application routes. It does not yet enable
isolated model execution. An empty rights array in restricted mode denies all.
Read the JSON document, edit its mode/rights, then replace it with the same
membership_id and revision. A stale revision fails with 409; it is never retried.`,
}

var workspaceMemberAccessGetCmd = &cobra.Command{
	Use: "get <member-id-or-user-id>", Short: "Read the complete versioned access policy", Args: cobra.ExactArgs(1),
	Long: `Read a member's current policy. Use --format json to obtain the complete
document for the read-edit-set workflow; the default table shows a rights count.

  crewship --format json workspace member access get MEMBER > policy.json`,
	RunE: func(cmd *cobra.Command, args []string) error { return memberAccessCommand(cmd, args[0], false) },
}

var workspaceMemberAccessSetCmd = &cobra.Command{
	Use: "set <member-id-or-user-id>", Short: "Replace a policy from a reviewed JSON document", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return memberAccessCommand(cmd, args[0], true) },
}

func memberAccessCommand(cmd *cobra.Command, target string, replace bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	var policy access.Policy
	if replace {
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required (use - for stdin)")
		}
		var input io.Reader = cmd.InOrStdin()
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			input = f
		}
		d := json.NewDecoder(io.LimitReader(input, (64<<10)+1))
		d.DisallowUnknownFields()
		if d.Decode(&policy) != nil || d.Decode(new(any)) != io.EOF || policy.ID == "" || policy.Revision < 1 || policy.Rights == nil || (policy.Mode != "trusted" && policy.Mode != "restricted") {
			return fmt.Errorf("invalid policy: provide membership_id, revision, mode and explicit rights array")
		}
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	member, err := resolveWorkspaceMemberID(client, workspace, target)
	if err != nil {
		return err
	}
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/members/" + url.PathEscape(member) + "/access"
	if replace && policy.ID != member {
		return fmt.Errorf("policy membership_id differs from the selected member; read the current policy")
	}
	var responsePolicy access.Policy
	if replace {
		resp, err := client.Put(path, policy)
		if err != nil {
			return err
		}
		if err = cli.CheckError(resp); err != nil {
			return err
		}
		if err = cli.ReadJSON(resp, &responsePolicy); err != nil {
			return err
		}
	} else {
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err = cli.CheckError(resp); err != nil {
			return err
		}
		if err = cli.ReadJSON(resp, &responsePolicy); err != nil {
			return err
		}
	}
	return newFormatter().Auto(responsePolicy, []string{"MEMBER", "MODE", "REVISION", "RIGHTS"}, [][]string{{responsePolicy.ID, responsePolicy.Mode, strconv.FormatInt(responsePolicy.Revision, 10), strconv.Itoa(len(responsePolicy.Rights))}})
}

func init() {
	workspaceMemberAccessSetCmd.Flags().String("file", "", "Complete policy JSON file, or - for stdin")
	workspaceMemberAccessCmd.AddCommand(workspaceMemberAccessGetCmd, workspaceMemberAccessSetCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberAccessCmd)
}
