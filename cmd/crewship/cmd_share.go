package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/cli"
)

func newObjectShareCommand(remove bool) *cobra.Command {
	verb := "share"
	if remove {
		verb = "unshare"
	}
	cmd := &cobra.Command{
		Use: verb + " <routine|project> <resource-id>", Short: "Change one Guest's explicit shares on an object",
		Long: "Edit only this object's requested operations while preserving all other grants and issuer provenance. Trusted workspace OWNER/ADMIN only. No account conversion or conflict retry. Routine Run still requires agent grants and an installed runtime.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			operations, _ := cmd.Flags().GetStringSlice("operation")
			target, _ := cmd.Flags().GetString("with")
			reissue := false
			if !remove {
				reissue, _ = cmd.Flags().GetBool("reissue")
			}
			return changeObjectShare(args[0], args[1], target, operations, remove, reissue)
		},
	}
	cmd.Flags().String("with", "", "Existing member ID, user ID or exact email")
	cmd.Flags().StringSlice("operation", nil, "Operations to add/remove: routine run; project list,read (repeatable or comma-separated)")
	_ = cmd.MarkFlagRequired("with")
	_ = cmd.MarkFlagRequired("operation")
	if !remove {
		cmd.Flags().Bool("reissue", false, "Explicitly reissue the selected rights as the current administrator")
		cmd.AddCommand(&cobra.Command{
			Use: "list <routine|project> <resource-id>", Short: "List explicit object shares with issuer and active/inactive state", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error { return listObjectShares(args[0], args[1]) },
		})
	}
	return cmd
}

func sharingClient(kind, id string) (*cli.Client, string, error) {
	if kind != "routine" && kind != "project" {
		return nil, "", fmt.Errorf("resource kind must be routine or project")
	}
	if id == "" || id == "." || id == ".." {
		return nil, "", fmt.Errorf("provide a resource ID")
	}
	if err := requireAuth(); err != nil {
		return nil, "", err
	}
	if err := requireWorkspace(); err != nil {
		return nil, "", err
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	if workspace == "." || workspace == ".." {
		return nil, "", fmt.Errorf("invalid workspace ID")
	}
	return client, workspace, nil
}

func sharingRoster(client *cli.Client, workspace string) ([]workspaceMemberRow, error) {
	response, err := client.Get("/api/v1/workspaces/" + url.PathEscape(workspace) + "/members")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return nil, err
	}
	var members []workspaceMemberRow
	err = cli.ReadJSON(response, &members)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("administrative workspace roster unavailable")
	}
	for _, member := range members {
		if member.AccessMode != "trusted" && member.AccessMode != "restricted" {
			return nil, fmt.Errorf("workspace administration required to inspect shares")
		}
	}
	return members, nil
}

func sharingPolicy(client *cli.Client, workspace, member string) (access.Policy, string, error) {
	var policy access.Policy
	if member == "" || member == "." || member == ".." {
		return policy, "", fmt.Errorf("invalid membership ID")
	}
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/members/" + url.PathEscape(member) + "/access"
	response, err := client.Get(path)
	if err != nil {
		return policy, path, err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return policy, path, err
	}
	if err = cli.ReadJSON(response, &policy); err != nil {
		return policy, path, err
	}
	if policy.ID != member || policy.Revision < 1 || policy.Rights == nil {
		return policy, path, fmt.Errorf("membership changed or policy is incomplete; read the current policy")
	}
	return policy, path, nil
}

func changeObjectShare(kind, id, target string, operations []string, remove, reissue bool) error {
	// Validate every requested operation before issuing any HTTP request.
	if len(operations) == 0 || target == "" {
		return fmt.Errorf("--with and --operation are required")
	}
	selected := map[access.Right]bool{}
	for _, operation := range operations {
		if (kind == "routine" && operation != "run") || (kind == "project" && operation != "list" && operation != "read") {
			return fmt.Errorf("unsupported %s operation %q", kind, operation)
		}
		selected[access.Right{Kind: kind, ID: id, Operation: operation}] = true
	}
	client, workspace, err := sharingClient(kind, id)
	if err != nil {
		return err
	}
	members, err := sharingRoster(client, workspace)
	if err != nil {
		return err
	}
	memberID := ""
	for _, member := range members {
		userID := member.UserID
		if member.User != nil {
			userID = member.User.ID
		}
		if member.ID == target || userID == target || strings.EqualFold(member.email(), target) {
			if memberID != "" && memberID != member.ID {
				return fmt.Errorf("member reference is ambiguous; use the membership ID")
			}
			memberID = member.ID
		}
	}
	if memberID == "" {
		return fmt.Errorf("no workspace member matches %q", target)
	}
	policy, path, err := sharingPolicy(client, workspace, memberID)
	if err != nil {
		return err
	}
	if policy.Mode != "restricted" {
		return fmt.Errorf("trusted member access uses workspace roles; this command never changes account mode")
	}
	rights := make([]access.Right, 0, len(policy.Rights)+len(selected))
	present := map[access.Right]bool{}
	for _, right := range policy.Rights {
		if !remove || !selected[right] {
			rights = append(rights, right)
			present[right] = true
		}
	}
	var renew []access.Right
	if !remove {
		// Preserve CLI flag order for deterministic machine output and audit.
		for _, operation := range operations {
			right := access.Right{Kind: kind, ID: id, Operation: operation}
			if !present[right] {
				rights = append(rights, right)
				present[right] = true
			}
			if reissue && selected[right] {
				renew = append(renew, right)
				selected[right] = false
			}
		}
	}
	// Read-only provenance is deliberately excluded from the replacement.
	body := access.Policy{Membership: policy.Membership, Rights: rights, Reissue: renew}
	response, err := client.Put(path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return err // In particular, never retry a 409.
	}
	var updated access.Policy
	if err = cli.ReadJSON(response, &updated); err != nil {
		return err
	}
	if updated.ID != policy.ID || updated.Mode != policy.Mode || updated.Revision <= policy.Revision {
		return fmt.Errorf("save returned an unexpected policy snapshot; read the current policy")
	}
	document, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	return newFormatter().Auto(cli.RawJSON(document), []string{"MEMBER", "MODE", "REVISION"}, [][]string{{updated.ID, updated.Mode, fmt.Sprint(updated.Revision)}})
}

type objectShareRow struct {
	MemberID   string `json:"member_id" yaml:"member_id"`
	Email      string `json:"email" yaml:"email"`
	Kind       string `json:"kind" yaml:"kind"`
	ID         string `json:"id" yaml:"id"`
	Operation  string `json:"operation" yaml:"operation"`
	IssuerID   string `json:"issuer_id" yaml:"issuer_id"`
	IssuerName string `json:"issuer_name,omitempty" yaml:"issuer_name,omitempty"`
	CreatedAt  string `json:"created_at" yaml:"created_at"`
	Live       bool   `json:"live" yaml:"live"`
}

func listObjectShares(kind, id string) error {
	client, workspace, err := sharingClient(kind, id)
	if err != nil {
		return err
	}
	members, err := sharingRoster(client, workspace)
	if err != nil {
		return err
	}
	result := []objectShareRow{}
	rows := [][]string{}
	for _, member := range members {
		if member.AccessMode != "restricted" {
			continue
		}
		policy, _, err := sharingPolicy(client, workspace, member.ID)
		if err != nil {
			return err
		}
		for _, grant := range policy.GrantDetails {
			if grant.Kind == kind && grant.ID == id {
				state := "inactive"
				if grant.Live {
					state = "active"
				}
				result = append(result, objectShareRow{MemberID: member.ID, Email: member.email(), Kind: grant.Kind, ID: grant.ID, Operation: grant.Operation, IssuerID: grant.IssuerID, IssuerName: grant.IssuerName, CreatedAt: grant.CreatedAt, Live: grant.Live})
				rows = append(rows, []string{member.ID, member.email(), grant.Operation, grant.IssuerID, state})
			}
		}
	}
	return newFormatter().Auto(result, []string{"MEMBER", "EMAIL", "OPERATION", "ISSUER", "STATE"}, rows)
}

func init() {
	rootCmd.AddCommand(newObjectShareCommand(false), newObjectShareCommand(true))
}
