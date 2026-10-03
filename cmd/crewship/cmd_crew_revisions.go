package main

import (
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/spf13/cobra"
)

type environmentRevision struct {
	ID             string                           `json:"id" yaml:"id"`
	DefinitionHash string                           `json:"definition_hash" yaml:"definition_hash"`
	BuildHash      string                           `json:"build_hash" yaml:"build_hash"`
	ImageID        string                           `json:"image_id" yaml:"image_id"`
	Toolchain      *devcontainer.ToolchainInventory `json:"toolchain" yaml:"toolchain"`
	CreatedAt      string                           `json:"created_at" yaml:"created_at"`
}

var crewProvisionRevisionsCmd = &cobra.Command{
	Use:   "revisions <slug-or-id>",
	Short: "List the latest 100 built environment revisions",
	Long:  "List immutable build provenance. History does not prove an image is still stored or currently running, and does not retain an image for rollback.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/crews/" + crewID + "/provision/revisions")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			Revisions []environmentRevision `json:"revisions" yaml:"revisions"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		rows := make([][]string, 0, len(result.Revisions))
		for _, rev := range result.Revisions {
			rows = append(rows, []string{rev.ID, rev.CreatedAt, rev.ImageID, rev.DefinitionHash})
		}
		return newFormatter().Auto(result.Revisions, []string{"REVISION", "BUILT", "IMAGE ID", "DEFINITION HASH"}, rows)
	},
}

func init() { crewProvisionCmd.AddCommand(crewProvisionRevisionsCmd) }
