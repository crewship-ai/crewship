package main

// GET /api/v1/workspaces/{workspaceId}/artifact-creation-policy workspace artifact-policy show
// PUT /api/v1/workspaces/{workspaceId}/artifact-creation-policy workspace artifact-policy set

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func init() { workspaceCmd.AddCommand(newWorkspaceArtifactPolicyCommand()) }

func newWorkspaceArtifactPolicyCommand() *cobra.Command {
	command := &cobra.Command{Use: "artifact-policy", Short: "Manage agent artifact publication policy for the selected workspace"}
	show := &cobra.Command{Use: "show", Short: "Read the workspace switch, crew allowlist and policy version", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		path, err := workspaceArtifactPolicyPath(cmd, client)
		if err != nil {
			return err
		}
		response, err := client.Get(path)
		if err != nil {
			return err
		}
		return printPageOwnershipResponse(cmd, response)
	}}
	set := &cobra.Command{Use: "set", Short: "Replace the policy using an explicitly reviewed version", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		version, _ := cmd.Flags().GetInt64("base-version")
		if !cmd.Flags().Changed("base-version") || version < 0 {
			return fmt.Errorf("--base-version is required and must be nonnegative")
		}
		if !cmd.Flags().Changed("enabled") {
			return fmt.Errorf("--enabled=true or --enabled=false is required")
		}
		enabled, _ := cmd.Flags().GetBool("enabled")
		crews, _ := cmd.Flags().GetStringSlice("crew-id")
		clear, _ := cmd.Flags().GetBool("clear-crews")
		switch hasCrews := cmd.Flags().Changed("crew-id"); {
		case clear && hasCrews:
			return fmt.Errorf("provide --crew-id or --clear-crews, not both")
		case !clear && !hasCrews:
			return fmt.Errorf("--crew-id or --clear-crews is required: the allowlist is replaced, so name every crew or clear it explicitly")
		}
		if clear {
			crews = []string{}
		}
		seen := map[string]bool{}
		for _, crew := range crews {
			if crew == "" || strings.TrimSpace(crew) != crew || seen[crew] {
				return fmt.Errorf("--crew-id values must be nonempty, unique IDs")
			}
			seen[crew] = true
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		path, err := workspaceArtifactPolicyPath(cmd, client)
		if err != nil {
			return err
		}
		response, err := client.Put(path, map[string]any{"enabled": enabled, "crew_ids": crews, "base_version": version})
		if err != nil {
			return err
		}
		return printPageOwnershipResponse(cmd, response)
	}}
	set.Flags().Int64("base-version", 0, "Policy version returned by show; 0 means no policy has been saved")
	set.Flags().Bool("enabled", false, "Explicitly enable or disable agent artifact publication")
	set.Flags().StringSlice("crew-id", nil, "Complete allowlist of crew IDs, comma-separated or repeated")
	set.Flags().Bool("clear-crews", false, "Explicitly replace the allowlist with an empty list")
	command.AddCommand(show, set)
	return command
}

// workspaceArtifactPolicyPath resolves the selected workspace — an ID or a
// slug, from --workspace, the environment or the config — to its ID before the
// path is built. The raw slug in /workspaces/{id}/ is a 404, and this is a
// policy boundary, so a slug that cannot be resolved fails instead of falling
// back to the raw value.
func workspaceArtifactPolicyPath(cmd *cobra.Command, client *cli.Client) (string, error) {
	id, err := client.ResolveWorkspaceIDStrict(cmd.Context())
	if err != nil {
		return "", err
	}
	return "/api/v1/workspaces/" + pagePathEscape(id) + "/artifact-creation-policy", nil
}
