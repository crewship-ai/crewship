package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var integrationCmd = &cobra.Command{
	Use:     "integration",
	Aliases: []string{"intg", "mcp"},
	Short:   "Manage MCP server integrations",
}

// ==========================================
// Workspace-level integrations
// ==========================================

var intgListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace integrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/integrations")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var items []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			DisplayName     string `json:"display_name"`
			Transport       string `json:"transport"`
			Endpoint        string `json:"endpoint"`
			Enabled         bool   `json:"enabled"`
			AgentBindCount  int    `json:"agent_binding_count"`
			CrewServerCount int    `json:"crew_server_count"`
		}
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"NAME", "DISPLAY", "TRANSPORT", "ENDPOINT", "ENABLED", "AGENTS", "CREWS"}
		var rows [][]string
		for _, s := range items {
			enabled := "yes"
			if !s.Enabled {
				enabled = "no"
			}
			ep := s.Endpoint
			if ep == "" {
				ep = "-"
			}
			if len(ep) > 40 {
				ep = ep[:37] + "..."
			}
			rows = append(rows, []string{
				s.Name, s.DisplayName, s.Transport, ep, enabled,
				fmt.Sprintf("%d", s.AgentBindCount),
				fmt.Sprintf("%d", s.CrewServerCount),
			})
		}
		return f.Auto(items, headers, rows)
	},
}

var intgAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a workspace integration",
	Example: `  crewship integration add --name gmail --display "Google Gmail" --transport streamable-http --endpoint https://mcp.example.com/gmail
  crewship integration add --name local-tools --transport stdio --command "npx @modelcontextprotocol/server-github"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		display, _ := flags.GetString("display")
		transport, _ := flags.GetString("transport")
		endpoint, _ := flags.GetString("endpoint")
		command, _ := flags.GetString("command")
		icon, _ := flags.GetString("icon")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if transport == "" {
			transport = "streamable-http"
		}
		if display == "" {
			display = name
		}

		body := map[string]interface{}{
			"name":         name,
			"display_name": display,
			"transport":    transport,
		}
		if endpoint != "" {
			body["endpoint"] = endpoint
		}
		if command != "" {
			body["command"] = command
		}
		if icon != "" {
			body["icon"] = icon
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/integrations", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		cli.ReadJSON(resp, &created)
		fmt.Printf("Integration created: %s (%s)\n", created.Name, created.ID)
		return nil
	},
}

var intgRemoveCmd = &cobra.Command{
	Use:   "remove <id-or-name>",
	Short: "Remove a workspace integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		id, err := resolveIntegrationID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/integrations/" + id)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Integration %s deleted.\n", args[0])
		return nil
	},
}

var intgEnableCmd = &cobra.Command{
	Use:   "enable <id-or-name>",
	Short: "Enable a workspace integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return toggleIntegration(args[0], true)
	},
}

var intgDisableCmd = &cobra.Command{
	Use:   "disable <id-or-name>",
	Short: "Disable a workspace integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return toggleIntegration(args[0], false)
	},
}

// ==========================================
// Agent binding commands
// ==========================================

var intgBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Bind an integration to an agent with a credential",
	Example: `  crewship integration bind --agent pepa --server gmail --credential pepa-gmail-token
  crewship integration bind --agent franta --server gmail --credential franta-gmail-token --cred-type api_key`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		flags := cmd.Flags()
		agentSlug, _ := flags.GetString("agent")
		serverName, _ := flags.GetString("server")
		credName, _ := flags.GetString("credential")
		credType, _ := flags.GetString("cred-type")
		credHeader, _ := flags.GetString("cred-header")
		envVar, _ := flags.GetString("env-var")

		if agentSlug == "" || serverName == "" {
			return fmt.Errorf("--agent and --server are required")
		}

		client := newAPIClient()

		agentID, err := resolveAgentID(client, agentSlug)
		if err != nil {
			return err
		}
		serverID, err := resolveIntegrationID(client, serverName)
		if err != nil {
			return err
		}

		body := map[string]interface{}{
			"mcp_server_id":    serverID,
			"mcp_server_scope": "workspace",
		}
		if credName != "" {
			credID, err := resolveCredentialID(client, credName)
			if err != nil {
				return err
			}
			body["credential_id"] = credID
		}
		if credType != "" {
			body["cred_type"] = credType
		}
		if credHeader != "" {
			body["cred_header"] = credHeader
		}
		if envVar != "" {
			body["env_var_name"] = envVar
		}
		resp, err := client.Post("/api/v1/agents/"+agentID+"/integrations", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Agent %s bound to integration %s.\n", agentSlug, serverName)
		return nil
	},
}

var intgUnbindCmd = &cobra.Command{
	Use:   "unbind",
	Short: "Remove an integration binding from an agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		flags := cmd.Flags()
		agentSlug, _ := flags.GetString("agent")
		bindingID, _ := flags.GetString("binding-id")

		if agentSlug == "" || bindingID == "" {
			return fmt.Errorf("--agent and --binding-id are required")
		}
		client := newAPIClient()
		agentID, err := resolveAgentID(client, agentSlug)
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/agents/" + agentID + "/integrations/" + bindingID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Binding %s removed from agent %s.\n", bindingID, agentSlug)
		return nil
	},
}

var intgAgentListCmd = &cobra.Command{
	Use:   "agent-bindings <agent-slug>",
	Short: "List integration bindings for an agent",
	Args:  cobra.ExactArgs(1),
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
		resp, err := client.Get("/api/v1/agents/" + agentID + "/integrations")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var bindings []struct {
			ID             string  `json:"id"`
			MCPServerID    string  `json:"mcp_server_id"`
			MCPServerScope string  `json:"mcp_server_scope"`
			CredentialID   *string `json:"credential_id"`
			CredType       *string `json:"cred_type"`
			Enabled        bool    `json:"enabled"`
			ServerName     string  `json:"server_name"`
			ServerDisplay  string  `json:"server_display_name"`
			CredentialName *string `json:"credential_name"`
		}
		if err := cli.ReadJSON(resp, &bindings); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"BINDING ID", "SERVER", "SCOPE", "CREDENTIAL", "TYPE", "ENABLED"}
		var rows [][]string
		for _, b := range bindings {
			credName := "-"
			if b.CredentialName != nil {
				credName = *b.CredentialName
			}
			ct := "bearer"
			if b.CredType != nil && *b.CredType != "" {
				ct = *b.CredType
			}
			enabled := "yes"
			if !b.Enabled {
				enabled = "no"
			}
			rows = append(rows, []string{
				b.ID, b.ServerDisplay, b.MCPServerScope, credName, ct, enabled,
			})
		}
		return f.Auto(bindings, headers, rows)
	},
}

var intgResolveCmd = &cobra.Command{
	Use:   "resolve <agent-slug>",
	Short: "Show effective integrations for an agent (cascade resolution)",
	Args:  cobra.ExactArgs(1),
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
		resp, err := client.Get("/api/v1/agents/" + agentID + "/integrations/resolved")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var resolved []struct {
			ServerID    string  `json:"server_id"`
			Scope       string  `json:"scope"`
			Name        string  `json:"name"`
			DisplayName string  `json:"display_name"`
			Transport   string  `json:"transport"`
			Endpoint    *string `json:"endpoint"`
			CredID      *string `json:"credential_id"`
			CredName    *string `json:"credential_name"`
		}
		if err := cli.ReadJSON(resp, &resolved); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"NAME", "DISPLAY", "SCOPE", "TRANSPORT", "CREDENTIAL"}
		var rows [][]string
		for _, r := range resolved {
			cred := "-"
			if r.CredName != nil {
				cred = *r.CredName
			}
			rows = append(rows, []string{
				r.Name, r.DisplayName, r.Scope, r.Transport, cred,
			})
		}
		return f.Auto(resolved, headers, rows)
	},
}

// ==========================================
// Helpers
// ==========================================

func toggleIntegration(nameOrID string, enabled bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	id, err := resolveIntegrationID(client, nameOrID)
	if err != nil {
		return err
	}
	resp, err := client.Patch("/api/v1/integrations/"+id, map[string]interface{}{
		"enabled": enabled,
	})
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Integration %s %s.\n", nameOrID, state)
	return nil
}

func resolveIntegrationID(client *cli.Client, nameOrID string) (string, error) {
	resp, err := client.Get("/api/v1/integrations")
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ID == nameOrID || item.Name == nameOrID {
			return item.ID, nil
		}
	}
	return "", fmt.Errorf("integration %q not found", nameOrID)
}

func init() {
	// Subcommands
	integrationCmd.AddCommand(intgListCmd)
	integrationCmd.AddCommand(intgAddCmd)
	integrationCmd.AddCommand(intgRemoveCmd)
	integrationCmd.AddCommand(intgEnableCmd)
	integrationCmd.AddCommand(intgDisableCmd)
	integrationCmd.AddCommand(intgBindCmd)
	integrationCmd.AddCommand(intgUnbindCmd)
	integrationCmd.AddCommand(intgAgentListCmd)
	integrationCmd.AddCommand(intgResolveCmd)

	// add flags
	intgAddCmd.Flags().String("name", "", "Integration name (slug)")
	intgAddCmd.Flags().String("display", "", "Display name")
	intgAddCmd.Flags().String("transport", "streamable-http", "Transport: streamable-http or stdio")
	intgAddCmd.Flags().String("endpoint", "", "MCP server endpoint URL")
	intgAddCmd.Flags().String("command", "", "MCP server command (for stdio)")
	intgAddCmd.Flags().String("icon", "", "Lucide icon name")

	intgBindCmd.Flags().String("agent", "", "Agent slug (required)")
	intgBindCmd.Flags().String("server", "", "Integration name (required)")
	intgBindCmd.Flags().String("credential", "", "Credential name to bind")
	intgBindCmd.Flags().String("cred-type", "bearer", "Credential type: bearer, api_key, basic")
	intgBindCmd.Flags().String("cred-header", "", "Custom header for api_key type")
	intgBindCmd.Flags().String("env-var", "", "Env var name for stdio credential injection (e.g. GITHUB_TOKEN)")

	intgUnbindCmd.Flags().String("agent", "", "Agent slug (required)")
	intgUnbindCmd.Flags().String("binding-id", "", "Binding ID to remove (required)")
}
