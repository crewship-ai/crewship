package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/manifest"
)

// exportCrewCmd is `crewship export crew <slug>` — pulls a crew's
// current state out of the workspace as a kind=Crew manifest. The
// output is the round-trip partner of `crewship apply -f`: piping
// the output back into apply on a fresh workspace should reproduce
// the same crew (modulo computed fields like IDs and timestamps).
//
// The output is YAML with a yaml-language-server $schema hint so
// the user gets IDE autocomplete when they edit the file. JSON
// output is reachable by piping through `yq -o json`; we don't ship
// a dedicated flag because YAML is the canonical authored form.
var exportCrewCmd = &cobra.Command{
	Use:   "crew <slug>",
	Short: "Export a crew as a kind=Crew manifest (YAML)",
	Long: `Pull a crew's current state and render it as a kind=Crew manifest
that can be re-applied with 'crewship apply -f'. Credential values
are NEVER included in the export — only the credential slots are
emitted so the file is safe to commit and share.

Examples:
  crewship export crew code-review > code-review.crew.yaml
  crewship export crew code-review --no-credentials
  crewship export crew code-review -o ./manifests/code-review.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runExportCrew,
}

// exportWorkspaceCmd is `crewship export workspace` — pulls every
// crew in the active workspace into a single kind=Workspace bundle.
// Workspace-level deduping is applied: skills and credentials used
// by any agent in any crew are lifted to the workspace scope so
// consumers see one declaration each. Per-crew specs still carry
// the agent → skill / agent → credential refs that resolve against
// the merged workspace+crew scope at apply-time.
var exportWorkspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Export the active workspace as a kind=Workspace manifest",
	Long: `Pull every crew in the active workspace and render them as a single
kind=Workspace manifest. Workspace-level skills and credentials are
deduplicated. Useful for backing up the whole setup or copying it to
another instance.

Examples:
  crewship export workspace > full.workspace.yaml
  crewship export workspace --no-credentials --output backup.yaml`,
	RunE: runExportWorkspace,
}

func init() {
	exportCrewCmd.Flags().StringP("output", "o", "", "Write to file instead of stdout")
	exportCrewCmd.Flags().Bool("no-credentials", false, "Strip credential slots from output (consumers must declare their own)")
	exportCrewCmd.Flags().Bool("no-skill-bodies", false, "Skip skill bodies (slug-only references)")

	exportWorkspaceCmd.Flags().StringP("output", "o", "", "Write to file instead of stdout")
	exportWorkspaceCmd.Flags().Bool("no-credentials", false, "Strip credential slots from output")
	exportWorkspaceCmd.Flags().Bool("no-skill-bodies", false, "Skip skill bodies (slug-only references)")

	exportCmd.AddCommand(exportCrewCmd)
	exportCmd.AddCommand(exportWorkspaceCmd)
}

func runExportWorkspace(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	noCreds, _ := cmd.Flags().GetBool("no-credentials")
	noSkillBodies, _ := cmd.Flags().GetBool("no-skill-bodies")

	client := manifest.NewClientFromCLI(newAPIClient())
	opts := manifest.DefaultExportOptions()
	opts.IncludeCredentials = !noCreds
	opts.IncludeSkillBodies = !noSkillBodies

	yaml, err := manifest.ExportWorkspace(cmd.Context(), client, opts)
	if err != nil {
		return fmt.Errorf("export workspace: %w", err)
	}

	if output == "" {
		fmt.Print(yaml)
		return nil
	}
	if err := os.WriteFile(output, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", output)
	return nil
}

func runExportCrew(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	noCreds, _ := cmd.Flags().GetBool("no-credentials")
	noSkillBodies, _ := cmd.Flags().GetBool("no-skill-bodies")

	client := manifest.NewClientFromCLI(newAPIClient())
	opts := manifest.DefaultExportOptions()
	opts.IncludeCredentials = !noCreds
	opts.IncludeSkillBodies = !noSkillBodies

	yaml, err := manifest.ExportCrew(cmd.Context(), client, args[0], opts)
	if err != nil {
		return fmt.Errorf("export crew %q: %w", args[0], err)
	}

	if output == "" {
		fmt.Print(yaml)
		return nil
	}
	if err := os.WriteFile(output, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", output)
	return nil
}
