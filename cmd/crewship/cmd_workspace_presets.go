package main

import (
	"encoding/json"
	"net/url"
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
		Presets []access.CatalogEntry `json:"presets"`
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

func init() { workspaceMemberAccessCmd.AddCommand(workspaceAccessPresetsCmd) }

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
