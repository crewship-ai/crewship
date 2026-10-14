package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

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
document for the read-edit-set workflow; the default table shows issuers and active/inactive rights.

  crewship --format json workspace member access get MEMBER > policy.json`,
	RunE: func(cmd *cobra.Command, args []string) error { return memberAccessCommand(cmd, args[0], false) },
}

var workspaceMemberAccessSetCmd = &cobra.Command{
	Use: "set <member-id-or-user-id>", Short: "Replace a policy from a reviewed JSON document", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return memberAccessCommand(cmd, args[0], true) },
}

type memberEffectiveExplanation struct {
	MembershipID      string                            `json:"membership_id" yaml:"membership_id"`
	Mode              string                            `json:"mode" yaml:"mode"`
	Revision          int64                             `json:"revision" yaml:"revision"`
	Explanation       string                            `json:"explanation" yaml:"explanation"`
	AccountRestricted bool                              `json:"account_restricted" yaml:"account_restricted"`
	RestrictionScope  string                            `json:"restriction_scope" yaml:"restriction_scope"`
	Decisions         []memberAccessExplanationDecision `json:"decisions" yaml:"decisions"`
}
type memberAccessExplanationDecision struct {
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	Kind      string `json:"kind" yaml:"kind"`
	ID        string `json:"id" yaml:"id"`
	Operation string `json:"operation" yaml:"operation"`
	State     string `json:"state" yaml:"state"`
	Reason    string `json:"reason" yaml:"reason"`
}

var workspaceMemberAccessExplainCmd = &cobra.Command{
	Use: "explain <member-id-or-user-id>", Short: "Explain a member's current grants and execution prerequisites", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		workspace := client.GetWorkspaceID()
		member, err := resolveWorkspaceMemberID(client, workspace, args[0])
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/workspaces/" + url.PathEscape(workspace) + "/members/" + url.PathEscape(member) + "/access/effective")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		var result memberEffectiveExplanation
		if err = cli.ReadJSON(response, &result); err != nil {
			return err
		}
		rows := [][]string{}
		for _, decision := range result.Decisions {
			rows = append(rows, []string{decision.Kind, decision.resourceName(), decision.Operation, decision.State, decision.Reason})
		}
		if len(rows) == 0 {
			rows = append(rows, []string{"", "", "", "", result.Explanation})
		}
		return newFormatter().Auto(result, []string{"KIND", "RESOURCE", "OPERATION", "STATE", "REASON"}, rows)
	},
}

func (d memberAccessExplanationDecision) resourceName() string {
	if d.Name != "" {
		return d.Name
	}
	return d.ID
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
		d := json.NewDecoder(io.LimitReader(input, (256<<10)+1))
		d.DisallowUnknownFields()
		if d.Decode(&policy) != nil || d.Decode(new(any)) != io.EOF || policy.ID == "" || policy.Revision < 1 || policy.Rights == nil || (policy.Mode != "trusted" && policy.Mode != "restricted") {
			return fmt.Errorf("invalid policy: provide membership_id, revision, mode and explicit rights array")
		}
	}
	if replace {
		rights, _ := cmd.Flags().GetStringArray("reissue")
		for _, raw := range rights {
			parts := strings.Split(raw, ":")
			if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
				return fmt.Errorf("invalid --reissue %q: use kind:resource-id:operation", raw)
			}
			right := access.Right{Kind: parts[0], ID: parts[1], Operation: parts[2]}
			found := false
			for _, r := range policy.Rights {
				if r == right {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("--reissue right must appear in policy rights")
			}
			duplicate := false
			for _, r := range policy.Reissue {
				if r == right {
					duplicate = true
					break
				}
			}
			if !duplicate {
				policy.Reissue = append(policy.Reissue, right)
			}
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
	rows := [][]string{}
	for _, d := range responsePolicy.GrantDetails {
		state := "inactive"
		if d.Live {
			state = "active"
		}
		rows = append(rows, []string{responsePolicy.ID, responsePolicy.Mode, strconv.FormatInt(responsePolicy.Revision, 10), d.Kind, d.ID, d.Operation, d.IssuerID, state})
	}
	if len(rows) == 0 {
		rows = append(rows, []string{responsePolicy.ID, responsePolicy.Mode, strconv.FormatInt(responsePolicy.Revision, 10), "—", "—", "—", "—", "—"})
	}
	return newFormatter().Auto(responsePolicy, []string{"MEMBER", "MODE", "REVISION", "KIND", "RESOURCE", "OPERATION", "ISSUER", "STATE"}, rows)
}

func init() {
	workspaceMemberAccessSetCmd.Flags().StringArray("reissue", nil, "Explicitly reissue a policy right as the current administrator (kind:resource-id:operation; repeatable)")
	workspaceMemberAccessSetCmd.Flags().String("file", "", "Complete policy JSON file, or - for stdin")
	workspaceMemberAccessCmd.AddCommand(workspaceMemberAccessGetCmd, workspaceMemberAccessSetCmd, workspaceMemberAccessExplainCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberAccessCmd)
}
