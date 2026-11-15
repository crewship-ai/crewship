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
		body := map[string]any{"enabled": enabled, "crew_ids": crews, "base_version": version}
		limits := map[string]int{}
		for flag, key := range map[string]string{"chat-run-create-limit": "chat_run_create_limit", "run-read-limit": "run_read_limit", "crew-hourly-page-limit": "crew_hourly_page_limit"} {
			if cmd.Flags().Changed(flag) {
				v, _ := cmd.Flags().GetInt(flag)
				limits[key] = v
			}
		}
		if len(limits) > 0 {
			body["limits"] = limits
		}
		if cmd.Flags().Changed("save-requires-run-token") {
			v, _ := cmd.Flags().GetBool("save-requires-run-token")
			body["save_requires_run_token"] = v
		}
		response, err := client.Put(path, body)
		if err != nil {
			return err
		}
		return printPageOwnershipResponse(cmd, response)
	}}
	set.Flags().Int64("base-version", 0, "Policy version returned by show; 0 means no policy has been saved")
	set.Flags().Bool("enabled", false, "Explicitly enable or disable agent artifact publication")
	set.Flags().StringSlice("crew-id", nil, "Complete allowlist of crew IDs, comma-separated or repeated")
	set.Flags().Bool("clear-crews", false, "Explicitly replace the allowlist with an empty list")
	set.Flags().Int("chat-run-create-limit", 0, "Artifacts one chat run may create (1-20, default 3); omitted keeps the current value")
	set.Flags().Int("run-read-limit", 0, "Distinct artifacts one run may read through the agent's own access (1-500, default 50)")
	set.Flags().Int("crew-hourly-page-limit", 0, "Pages one crew may create per hour through the internal save path (1-1000, default 20)")
	set.Flags().Bool("save-requires-run-token", false, "Refuse an agent page save without the agent run token (403 run_token_required); omitted keeps the current value")
	command.Long = `Read or replace the workspace ceiling for agent work on artifacts: whether
agents may create or change artifacts on a person's behalf, and which crews
may do so. Requires the OWNER or ADMIN role and a token with workspace:admin.

With agent access modes on (feature flag artifact_agent_access_modes), the
policy also carries limits: artifacts one chat run may create, distinct
artifacts one run may read through the agent's own access, and pages one crew
may create per hour. A workspace that never saved a policy uses the defaults
(3, 50, 20) and, with the flag on, allows every live crew.

A page save from the crew sidecar that carries the agent run token counts
against its run's create limit. One without the token (an older sidecar) is
accepted under the crew hourly limit only, until --save-requires-run-token=true
makes it refused. The token is accounting, not a boundary between the agents
of one crew.`
	show.Example = "  crewship workspace artifact-policy show -f json"
	set.Long = `Replace the whole policy. Pass the version from show as --base-version;
a newer saved policy returns 409 stale_policy. Give --enabled explicitly and
exactly one of --crew-id (complete allowlist) or --clear-crews.`
	set.Example = "  crewship workspace artifact-policy set --base-version 0 --enabled=true --crew-id <crew-id>\n  crewship workspace artifact-policy set --base-version 1 --enabled=false --clear-crews\n  crewship workspace artifact-policy set --base-version 2 --enabled=true --crew-id <crew-id> --chat-run-create-limit 5\n  crewship workspace artifact-policy set --base-version 3 --enabled=true --crew-id <crew-id> --save-requires-run-token=true"
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
