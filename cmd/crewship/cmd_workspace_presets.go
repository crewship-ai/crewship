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

var workspaceAccessPresetsCmd = &cobra.Command{Use: "presets", Short: "List immutable resource-access bundles", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	resp, err := client.Get("/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + "/access/presets")
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var catalog struct {
		Presets []access.CatalogEntry `json:"presets" yaml:"presets"`
	}
	if err := cli.ReadJSON(resp, &catalog); err != nil {
		return err
	}
	rows := [][]string{}
	for _, entry := range catalog.Presets {
		for _, level := range entry.Definition.Levels {
			b, _ := json.Marshal(level.Operations)
			rows = append(rows, []string{entry.Definition.ID, strconv.Itoa(entry.Definition.Version), level.Level, string(b), entry.Hash})
		}
	}
	return formatPresetDocument(catalog, []string{"PRESET", "VERSION", "LEVEL", "OPERATIONS", "HASH"}, rows)
}}

var workspaceAccessPresetCmd = &cobra.Command{Use: "preset", Short: "Preview an explicit resource-access bundle"}
var workspaceAccessPresetPreviewCmd = &cobra.Command{Use: "preview MEMBER", Short: "Preview a preset without changing permissions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	var selection access.PresetRequest
	if err := readExplicitPresetJSON(cmd, &selection); err != nil {
		return err
	}
	if selection.MembershipID == "" || selection.Revision < 1 || selection.Resource.Kind == "" || selection.Resource.ID == "" || selection.PresetID == "" || selection.Version < 1 || selection.Level == "" {
		return fmt.Errorf("invalid preset selection: provide current membership_id, revision, resource, preset_id, preset_version and level")
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	member, err := resolveWorkspaceMemberID(client, workspace, args[0])
	if err != nil {
		return err
	}
	if member != selection.MembershipID {
		return fmt.Errorf("selection membership_id differs from the current member; no preview requested")
	}
	resp, err := client.Post("/api/v1/workspaces/"+url.PathEscape(workspace)+"/members/"+url.PathEscape(member)+"/access/presets/preview", selection)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var preview access.PresetPreview
	if err := cli.ReadJSON(resp, &preview); err != nil {
		return err
	}
	rows := [][]string{{"preset", preview.Selection.Resource.Kind, preview.Selection.Resource.ID, preview.Selection.Level, "", "", "", fmt.Sprintf("v%d · %s", preview.Selection.Version, preview.DefinitionHash)}}
	if choice := preview.CurrentSelection; choice != nil {
		rows = append(rows, []string{"saved", choice.Kind, choice.ResourceID, choice.Level, "", choice.SelectedAt, "", choice.State + ": saved intent, not effective authority"})
	} else {
		rows = append(rows, []string{"saved", "", "", "custom", "", "", "", "No explicit preset was previously selected"})
	}
	for _, change := range preview.Changes {
		rows = append(rows, []string{change.Action, change.Right.Kind, change.Right.ID, change.Right.Operation, change.IssuerID, change.CreatedAt, strconv.FormatBool(change.Live), ""})
	}
	for _, right := range preview.OrthogonalRights {
		rows = append(rows, []string{"orthogonal", right.Kind, right.ID, right.Operation, right.IssuerID, right.CreatedAt, strconv.FormatBool(right.Live), "Preserved unchanged"})
	}
	for _, warning := range preview.Warnings {
		message := warning.Message
		if warning.BeforeEligible != nil && warning.AfterEligible != nil {
			message += fmt.Sprintf(" Eligibility %t → %t.", *warning.BeforeEligible, *warning.AfterEligible)
		}
		rows = append(rows, []string{"warning", "", "", warning.Code, "", "", "", message})
	}
	rows = append(rows, []string{"digest", "", "", preview.Digest, "", "", "", "Confirmation checksum only; no permissions changed"})
	return formatPresetDocument(preview, []string{"ACTION", "KIND", "RESOURCE", "OPERATION", "ISSUER", "CREATED", "LIVE", "CONSEQUENCE"}, rows)
}}

func init() {
	workspaceAccessPresetPreviewCmd.Flags().String("file", "", "Explicit preset selection JSON file, or - for stdin")
	workspaceAccessPresetCmd.AddCommand(workspaceAccessPresetPreviewCmd)
	workspaceMemberAccessCmd.AddCommand(workspaceAccessPresetsCmd, workspaceAccessPresetCmd)
}

// Keep API field names in YAML without changing existing commands' YAML keys
// or interpreting int64 revisions as floating-point numbers. Other formats
// retain the typed document, so NDJSON emits one document rather than bytes.
func formatPresetDocument(document any, headers []string, rows [][]string) error {
	formatter := newFormatter()
	if formatter.Format == "yaml" {
		encoded, err := json.Marshal(document)
		if err != nil {
			return err
		}
		return formatter.YAML(cli.RawJSON(encoded))
	}
	return formatter.Auto(document, headers, rows)
}

// Read one explicit bounded document without silently repairing consent.
func readExplicitPresetJSON(cmd *cobra.Command, document any) error {
	file, _ := cmd.Flags().GetString("file")
	if file == "" {
		return fmt.Errorf("--file is required (use - for stdin)")
	}
	input := cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		input = f
	}
	limited := &io.LimitedReader{R: input, N: (256 << 10) + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if decoder.Decode(document) != nil || decoder.Decode(new(any)) != io.EOF || limited.N == 0 {
		return fmt.Errorf("provide one bounded preset JSON document without unknown or trailing fields")
	}
	return nil
}
