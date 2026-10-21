package main

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/cli"
)

type confirmedPreset struct {
	Selection access.PresetRequest `json:"selection" yaml:"selection"`
	Digest    string               `json:"confirmation_digest" yaml:"confirmation_digest"`
}

var workspaceAccessPresetApplyCmd = &cobra.Command{Use: "apply MEMBER", Short: "Apply an explicitly reviewed preset preview", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	var input confirmedPreset
	if err := readExplicitPresetJSON(cmd, &input); err != nil {
		return err
	}
	selection := input.Selection
	decoded, err := hex.DecodeString(input.Digest)
	if err != nil || len(decoded) != 32 || selection.MembershipID == "" || selection.Revision < 1 || selection.Resource.ID == "" || selection.Resource.Kind == "" || selection.PresetID == "" || selection.Version < 1 || selection.Level == "" {
		return fmt.Errorf("provide an explicit selection and its preview confirmation_digest")
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	member, err := resolveWorkspaceMemberID(client, workspace, args[0])
	if err != nil {
		return err
	}
	if member != selection.MembershipID {
		return fmt.Errorf("selection membership_id differs from the current member; no preset applied")
	}
	response, err := client.Post("/api/v1/workspaces/"+url.PathEscape(workspace)+"/members/"+url.PathEscape(member)+"/access/presets/apply", input)
	if err != nil {
		return err
	}
	if err := cli.CheckError(response); err != nil {
		return err
	}
	var policy access.Policy
	if err := cli.ReadJSON(response, &policy); err != nil {
		return err
	}
	rows := [][]string{}
	if policy.PresetSelections != nil {
		for _, choice := range *policy.PresetSelections {
			rows = append(rows, []string{"saved", choice.Kind, choice.ResourceID, choice.Level, "", choice.SelectedAt, choice.State + " intent"})
		}
	}
	for _, grant := range policy.GrantDetails {
		rows = append(rows, []string{"grant", grant.Kind, grant.ID, grant.Operation, grant.IssuerID, grant.CreatedAt, strconv.FormatBool(grant.Live)})
	}
	return formatPresetDocument(policy, []string{"ENTRY", "KIND", "RESOURCE", "OPERATION", "ISSUER", "CREATED", "LIVE/STATE"}, rows)
}}

func init() {
	workspaceAccessPresetApplyCmd.Flags().String("file", "", "Confirmed selection and preview digest JSON file, or - for stdin")
	workspaceAccessPresetCmd.AddCommand(workspaceAccessPresetApplyCmd)
}
