package main

// agent mcp list/add/update/remove — CRUD over per-agent MCP server
// bindings. Server-side handlers live in internal/api/agent_bindings.go;
// routes are mounted at /api/v1/agents/{agentId}/integrations… (the
// handler comments still say /mcp-bindings but the router_crews.go path
// is /integrations — bindings == integrations on the wire).
//
// Bound as subcommands of the existing `agentMCPCmd` (cmd_mcp.go) so the
// existing `agent mcp <slug>` config form keeps working when no
// subcommand is invoked.

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// agentMCPBinding mirrors the response body returned by ListAgentBindings.
// Pointers for nullable columns so missing fields render as "-" rather
// than empty strings the user could mistake for "set to empty".
type agentMCPBinding struct {
	ID             string  `json:"id"`
	AgentID        string  `json:"agent_id"`
	MCPServerID    string  `json:"mcp_server_id"`
	MCPServerScope string  `json:"mcp_server_scope"`
	CredentialID   *string `json:"credential_id"`
	CredentialName *string `json:"credential_name"`
	CredType       *string `json:"cred_type"`
	Enabled        bool    `json:"enabled"`
	ServerName     string  `json:"server_name"`
	ServerDisplay  string  `json:"server_display_name"`
	CreatedAt      string  `json:"created_at"`
}

var agentMCPListCmd = &cobra.Command{
	Use:   "list <agent-slug-or-id>",
	Short: "List all MCP server bindings for an agent",
	Long: `Show which MCP servers are bound to this agent, with each binding's
enabled flag, credential (if any), and scope (workspace vs crew).

Examples:
  crewship agent mcp list viktor
  crewship agent mcp list viktor --format json | jq '.[] | select(.enabled)'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		var bindings []agentMCPBinding
		if err := getJSON(client, "/api/v1/agents/"+agentID+"/integrations", &bindings); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "SERVER", "SCOPE", "ENABLED", "CREDENTIAL"}
		var rows [][]string
		for _, b := range bindings {
			cred := "-"
			if b.CredentialName != nil && *b.CredentialName != "" {
				cred = *b.CredentialName
			}
			server := b.ServerDisplay
			if server == "" {
				server = b.ServerName
			}
			enabled := "no"
			if b.Enabled {
				enabled = "yes"
			}
			rows = append(rows, []string{
				shortBindingID(b.ID), server, b.MCPServerScope, enabled, cred,
			})
		}
		return f.Auto(bindings, headers, rows)
	},
}

var agentMCPAddCmd = &cobra.Command{
	Use:   "add <agent-slug-or-id> <integration-id>",
	Short: "Bind an MCP server (workspace or crew integration) to an agent",
	Long: `Create a new binding between an agent and an MCP server. The
<integration-id> refers to a workspace_mcp_servers.id or crew_mcp_servers.id;
choose with --scope (default: workspace).

Examples:
  crewship agent mcp add viktor cs_jira_abc --scope crew
  crewship agent mcp add viktor wms_github_def --credential cred_xyz
  crewship agent mcp add viktor wms_github_def --enabled=false`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		scope, _ := cmd.Flags().GetString("scope")
		if scope != "workspace" && scope != "crew" {
			return fmt.Errorf("--scope must be 'workspace' or 'crew' (got %q)", scope)
		}

		body := map[string]any{
			"mcp_server_id":    args[1],
			"mcp_server_scope": scope,
		}
		if cred, _ := cmd.Flags().GetString("credential"); cred != "" {
			body["credential_id"] = cred
		}
		if credType, _ := cmd.Flags().GetString("cred-type"); credType != "" {
			body["cred_type"] = credType
		}
		if envVar, _ := cmd.Flags().GetString("env-var"); envVar != "" {
			body["env_var_name"] = envVar
		}
		if cmd.Flags().Changed("enabled") {
			enabled, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = enabled
		}

		var created struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := postJSON(client, "/api/v1/agents/"+agentID+"/integrations", body, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Bound integration %s to agent %s (binding id: %s, enabled=%v).",
			args[1], args[0], created.ID, created.Enabled))
		return nil
	},
}

var agentMCPUpdateCmd = &cobra.Command{
	Use:   "update <agent-slug-or-id> <binding-id>",
	Short: "Update an existing MCP binding (enabled flag, credential, cred type)",
	Long: `Patch an existing binding. At least one of --enabled / --credential /
--cred-type / --env-var / --tools must be provided.

The --tools flag is accepted as a comma-separated allowlist that ends up in
config_override_json (which the server merges with the integration's base
config). Empty string clears the override.

Examples:
  crewship agent mcp update viktor agm_xyz --enabled=false
  crewship agent mcp update viktor agm_xyz --credential cred_new
  crewship agent mcp update viktor agm_xyz --tools "issue.create,issue.list"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]any{}
		if cmd.Flags().Changed("enabled") {
			enabled, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = enabled
		}
		if cmd.Flags().Changed("credential") {
			cred, _ := cmd.Flags().GetString("credential")
			body["credential_id"] = cred
		}
		if cmd.Flags().Changed("cred-type") {
			ct, _ := cmd.Flags().GetString("cred-type")
			body["cred_type"] = ct
		}
		if cmd.Flags().Changed("env-var") {
			ev, _ := cmd.Flags().GetString("env-var")
			body["env_var_name"] = ev
		}
		if cmd.Flags().Changed("tools") {
			tools, _ := cmd.Flags().GetString("tools")
			if tools == "" {
				body["config_override_json"] = ""
			} else {
				// Minimal JSON wrapper — the server stores the string verbatim
				// and the runtime side parses {tools:[…]} to filter exposed
				// tools. Quoting via fmt to avoid pulling in encoding/json
				// for a list of slugs.
				parts := strings.Split(tools, ",")
				clean := make([]string, 0, len(parts))
				for _, p := range parts {
					if t := strings.TrimSpace(p); t != "" {
						clean = append(clean, fmt.Sprintf("%q", t))
					}
				}
				body["config_override_json"] = `{"tools":[` + strings.Join(clean, ",") + `]}`
			}
		}

		if len(body) == 0 {
			return fmt.Errorf("nothing to update — pass at least one of --enabled, --credential, --cred-type, --env-var, --tools")
		}

		resp, err := client.Patch("/api/v1/agents/"+agentID+"/integrations/"+args[1], body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Updated binding %s on agent %s.", args[1], args[0]))
		return nil
	},
}

var agentMCPRemoveCmd = &cobra.Command{
	Use:     "remove <agent-slug-or-id> <binding-id>",
	Aliases: []string{"rm", "delete"},
	Short:   "Delete an MCP binding from an agent",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/agents/"+agentID+"/integrations/"+args[1]); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Removed binding %s from agent %s.", args[1], args[0]))
		return nil
	},
}

// shortBindingID trims CUIDs to 12 chars for table display. Full ID stays
// available in JSON output for scripting.
func shortBindingID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func init() {
	agentMCPAddCmd.Flags().String("scope", "workspace", "Integration scope: 'workspace' or 'crew'")
	agentMCPAddCmd.Flags().String("credential", "", "Credential ID to attach to this binding")
	agentMCPAddCmd.Flags().String("cred-type", "", "Credential type: bearer | api_key | basic")
	agentMCPAddCmd.Flags().String("env-var", "", "Env var name to inject the credential under (stdio MCP servers)")
	agentMCPAddCmd.Flags().Bool("enabled", true, "Whether the binding is active at creation time")

	agentMCPUpdateCmd.Flags().Bool("enabled", false, "Set enabled/disabled state")
	agentMCPUpdateCmd.Flags().String("credential", "", "Replace bound credential (empty string unsets)")
	agentMCPUpdateCmd.Flags().String("cred-type", "", "Replace credential type")
	agentMCPUpdateCmd.Flags().String("env-var", "", "Replace env var name (empty string unsets)")
	agentMCPUpdateCmd.Flags().String("tools", "", "Comma-separated tool allowlist (empty clears override)")

	// All four hang off the existing `agent mcp` parent; the bare
	// `agent mcp <slug>` config form keeps working since none of the
	// subcommand names ('list', 'add', 'update', 'remove') collide with
	// realistic agent slugs.
	agentMCPCmd.AddCommand(agentMCPListCmd)
	agentMCPCmd.AddCommand(agentMCPAddCmd)
	agentMCPCmd.AddCommand(agentMCPUpdateCmd)
	agentMCPCmd.AddCommand(agentMCPRemoveCmd)
}
