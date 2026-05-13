package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Note on naming: the server route is
// PATCH /api/v1/agents/{agentId}/integrations/{integrationId}, but the
// {integrationId} segment is actually the agent_mcp_bindings row id.
// The existing `unbind` command uses --binding-id for the same value;
// the task spec calls it <integration-id>, so we keep that surface
// name and document the reality in the long help.

var intgAgentUpdateBindingCmd = &cobra.Command{
	Use:   "update-binding <agent-slug> <binding-id>",
	Short: "Update an agent's integration binding (credential, type, env var, enabled)",
	Long: `Patch an agent_mcp_bindings row.

<binding-id> is the binding row's ID, as printed by
'crewship integration agent-bindings <agent-slug>' under the BINDING ID
column. This is NOT the workspace integration's ID — bindings are 1:1
with (agent, server) pairs.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		flags := cmd.Flags()
		body := map[string]interface{}{}
		if flags.Changed("credential") {
			credName, _ := flags.GetString("credential")
			if credName == "" {
				body["credential_id"] = ""
			} else {
				credID, err := resolveCredentialID(client, credName)
				if err != nil {
					return err
				}
				body["credential_id"] = credID
			}
		}
		if flags.Changed("cred-type") {
			v, _ := flags.GetString("cred-type")
			body["cred_type"] = v
		}
		if flags.Changed("cred-header") {
			v, _ := flags.GetString("cred-header")
			body["cred_header"] = v
		}
		if flags.Changed("env-var-name") {
			v, _ := flags.GetString("env-var-name")
			body["env_var_name"] = v
		}
		if flags.Changed("enabled") {
			v, _ := flags.GetBool("enabled")
			body["enabled"] = v
		}
		if len(body) == 0 {
			return fmt.Errorf("no fields to update (use --credential, --cred-type, --cred-header, --env-var-name, or --enabled)")
		}

		resp, err := client.Patch("/api/v1/agents/"+agentID+"/integrations/"+args[1], body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Binding %s on agent %s updated.\n", args[1], args[0])
		return nil
	},
}

func registerIntegrationAgentSubcommands() {
	integrationAgentCmd.AddCommand(intgAgentUpdateBindingCmd)
}

func registerIntegrationAgentFlags() {
	intgAgentUpdateBindingCmd.Flags().String("credential", "", "Credential name (empty string clears the binding)")
	intgAgentUpdateBindingCmd.Flags().String("cred-type", "", "Credential type: bearer, api_key, basic")
	intgAgentUpdateBindingCmd.Flags().String("cred-header", "", "Custom header for api_key type")
	intgAgentUpdateBindingCmd.Flags().String("env-var-name", "", "Environment variable name (empty clears it)")
	intgAgentUpdateBindingCmd.Flags().Bool("enabled", true, "Set enabled state")
}
