package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var integrationCmd = &cobra.Command{
	Use:     "integration",
	Aliases: []string{"intg"},
	Short:   "Manage MCP server integrations",
}

// integrationCrewCmd groups crew-scoped CRUD that lives at
// /api/v1/crews/{crewId}/integrations/*. Workspace-level commands are
// the older intg* set above; the crew commands mirror them under a
// nested verb so the user can disambiguate without inventing new flag
// shapes.
var integrationCrewCmd = &cobra.Command{
	Use:   "crew",
	Short: "Crew-scoped integration CRUD (list/create/update/delete/test)",
}

// integrationToolsCmd groups the per-tool enable/disable affordance
// (mcp_tool_bindings). Tool toggles only apply to crew-scoped servers.
var integrationToolsCmd = &cobra.Command{
	Use:   "tools",
	Short: "Manage per-tool enable/disable on crew-scoped integrations",
}

// integrationAgentCmd groups agent-binding mutations not covered by the
// existing top-level bind / unbind / agent-bindings commands. Today it
// only holds `update-binding`, but leaving the noun open makes room for
// any future per-binding read or patch surface without churning UI.
var integrationAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Mutate agent integration bindings (per-binding patches)",
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
		if err := cli.ReadJSON(resp, &created); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
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
// Workspace-level get + test
// ==========================================

var intgGetCmd = &cobra.Command{
	Use:   "get <id-or-name>",
	Short: "Show details for a workspace integration",
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
		resp, err := client.Get("/api/v1/integrations/" + id)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var s struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			DisplayName string  `json:"display_name"`
			Transport   string  `json:"transport"`
			Endpoint    *string `json:"endpoint"`
			Command     *string `json:"command"`
			Enabled     bool    `json:"enabled"`
			Icon        *string `json:"icon"`
			CreatedAt   string  `json:"created_at"`
			UpdatedAt   string  `json:"updated_at"`
		}
		if err := cli.ReadJSON(resp, &s); err != nil {
			return err
		}
		f := newFormatter()
		endpoint := "-"
		if s.Endpoint != nil {
			endpoint = *s.Endpoint
		}
		command := "-"
		if s.Command != nil {
			command = *s.Command
		}
		icon := "-"
		if s.Icon != nil {
			icon = *s.Icon
		}
		pairs := [][]string{
			{"ID", s.ID},
			{"Name", s.Name},
			{"Display", s.DisplayName},
			{"Transport", s.Transport},
			{"Endpoint", endpoint},
			{"Command", command},
			{"Enabled", yesNo(s.Enabled)},
			{"Icon", icon},
			{"Created", s.CreatedAt},
			{"Updated", s.UpdatedAt},
		}
		return f.AutoDetail(s, pairs)
	},
}

var intgTestCmd = &cobra.Command{
	Use:   "test <id-or-name>",
	Short: "Test the connection to a workspace integration",
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
		resp, err := client.Post("/api/v1/integrations/"+id+"/test", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result map[string]any
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		// Pretty-print whatever the server returned (success + latency,
		// or error + details). Test responses don't have a strict shape.
		return newFormatter().JSON(result)
	},
}

// intgCrewsOverviewCmd renders the cross-crew integrations overview —
// the same data backing the workspace Integrations page when grouped
// by crew. Useful for spotting which crews still reference a server
// you're about to delete.
var intgCrewsOverviewCmd = &cobra.Command{
	Use:   "crews-overview",
	Short: "List integrations across every crew in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/integrations/crews")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var items []map[string]any
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}
		return newFormatter().JSON(items)
	},
}

// ==========================================
// Crew-scoped CRUD
// ==========================================

var intgCrewListCmd = &cobra.Command{
	Use:   "list <crew-slug>",
	Short: "List integrations for a crew",
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
		resp, err := client.Get("/api/v1/crews/" + crewID + "/integrations")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var items []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			DisplayName string  `json:"display_name"`
			Transport   string  `json:"transport"`
			Endpoint    *string `json:"endpoint"`
			Enabled     bool    `json:"enabled"`
		}
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"ID", "NAME", "DISPLAY", "TRANSPORT", "ENDPOINT", "ENABLED"}
		var rows [][]string
		for _, s := range items {
			ep := "-"
			if s.Endpoint != nil {
				ep = *s.Endpoint
			}
			if len(ep) > 40 {
				ep = ep[:37] + "..."
			}
			rows = append(rows, []string{s.ID, s.Name, s.DisplayName, s.Transport, ep, yesNo(s.Enabled)})
		}
		return f.Auto(items, headers, rows)
	},
}

var intgCrewCreateCmd = &cobra.Command{
	Use:   "create <crew-slug>",
	Short: "Create a crew-scoped integration",
	Args:  cobra.ExactArgs(1),
	Example: `  crewship integration crew create engineering --name gmail --transport streamable-http --endpoint https://mcp.example.com/gmail
  crewship integration crew create engineering --name local-tools --transport stdio --command "npx @modelcontextprotocol/server-github"`,
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
		linkWorkspaceID, _ := flags.GetString("link-workspace-server")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if transport == "" {
			transport = "streamable-http"
		}
		if display == "" {
			display = name
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
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
		if linkWorkspaceID != "" {
			body["workspace_mcp_server_id"] = linkWorkspaceID
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/integrations", body)
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
		if err := cli.ReadJSON(resp, &created); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("Crew integration created: %s (%s)\n", created.Name, created.ID)
		return nil
	},
}

var intgCrewUpdateCmd = &cobra.Command{
	Use:   "update <crew-slug> <integration-id>",
	Short: "Update fields on a crew-scoped integration (display, transport, endpoint, ...)",
	Args:  cobra.ExactArgs(2),
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

		flags := cmd.Flags()
		body := map[string]interface{}{}
		if flags.Changed("display") {
			v, _ := flags.GetString("display")
			body["display_name"] = v
		}
		if flags.Changed("transport") {
			v, _ := flags.GetString("transport")
			body["transport"] = v
		}
		if flags.Changed("endpoint") {
			v, _ := flags.GetString("endpoint")
			body["endpoint"] = v
		}
		if flags.Changed("command") {
			v, _ := flags.GetString("command")
			body["command"] = v
		}
		if flags.Changed("icon") {
			v, _ := flags.GetString("icon")
			body["icon"] = v
		}
		if flags.Changed("enabled") {
			v, _ := flags.GetBool("enabled")
			body["enabled"] = v
		}
		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		resp, err := client.Patch("/api/v1/crews/"+crewID+"/integrations/"+args[1], body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Crew integration %s updated.\n", args[1])
		return nil
	},
}

var intgCrewDeleteCmd = &cobra.Command{
	Use:   "delete <crew-slug> <integration-id>",
	Short: "Delete a crew-scoped integration",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete crew integration %q from %q?", args[1], args[0])); err != nil {
			return err
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/crews/" + crewID + "/integrations/" + args[1])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Crew integration %s deleted.\n", args[1])
		return nil
	},
}

var intgCrewTestCmd = &cobra.Command{
	Use:   "test <crew-slug> <integration-id>",
	Short: "Test the connection for a crew-scoped integration",
	Args:  cobra.ExactArgs(2),
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
		resp, err := client.Post("/api/v1/crews/"+crewID+"/integrations/"+args[1]+"/test", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result map[string]any
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		return newFormatter().JSON(result)
	},
}

// ==========================================
// Per-tool toggles on crew-scoped integrations
// ==========================================

var intgToolsListCmd = &cobra.Command{
	Use:   "list <crew-slug> <integration-id>",
	Short: "List tool bindings for a crew-scoped integration",
	Args:  cobra.ExactArgs(2),
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
		resp, err := client.Get("/api/v1/crews/" + crewID + "/integrations/" + args[1] + "/tools")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var tools []struct {
			ID          string  `json:"id"`
			ToolName    string  `json:"tool_name"`
			Description *string `json:"description"`
			Enabled     bool    `json:"enabled"`
			UpdatedAt   string  `json:"updated_at"`
		}
		if err := cli.ReadJSON(resp, &tools); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"TOOL", "ENABLED", "DESCRIPTION", "UPDATED"}
		var rows [][]string
		for _, t := range tools {
			desc := "-"
			if t.Description != nil && *t.Description != "" {
				desc = *t.Description
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
			}
			rows = append(rows, []string{t.ToolName, yesNo(t.Enabled), desc, t.UpdatedAt})
		}
		return f.Auto(tools, headers, rows)
	},
}

// toggleCrewIntegrationTool is the shared body for `tools enable` and
// `tools disable`. Both PATCH the same row with a different enabled
// boolean, so the only thing the user-facing commands need to do is
// supply the value.
func toggleCrewIntegrationTool(crewSlug, integrationID, toolName string, enabled bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	crewID, err := resolveCrewID(client, crewSlug)
	if err != nil {
		return err
	}
	resp, err := client.Patch(
		"/api/v1/crews/"+crewID+"/integrations/"+integrationID+"/tools/"+toolName,
		map[string]interface{}{"enabled": enabled},
	)
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
	fmt.Printf("Tool %s on %s/%s %s.\n", toolName, crewSlug, integrationID, state)
	return nil
}

var intgToolsEnableCmd = &cobra.Command{
	Use:   "enable <crew-slug> <integration-id> <tool-name>",
	Short: "Enable a single tool on a crew-scoped integration",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return toggleCrewIntegrationTool(args[0], args[1], args[2], true)
	},
}

var intgToolsDisableCmd = &cobra.Command{
	Use:   "disable <crew-slug> <integration-id> <tool-name>",
	Short: "Disable a single tool on a crew-scoped integration",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return toggleCrewIntegrationTool(args[0], args[1], args[2], false)
	},
}

var intgToolsRefreshCmd = &cobra.Command{
	Use:   "refresh <crew-slug> <integration-id>",
	Short: "Reconcile tool bindings with the live MCP server discovery",
	Long: `Push the current discovered tool list to the server, which
upserts mcp_tool_bindings rows: new tools default to enabled, existing
ones keep their toggle state. Tools omitted from the payload are left
in place (never auto-revoked).

This command sends an empty list — the server treats that as a no-op
and just confirms the route works. For real refresh you would supply
the discovered tool array from the MCP probe.`,
	Args: cobra.ExactArgs(2),
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
		// TODO: scaffolding only — real callers (MCP probe / frontend)
		// should supply the discovered tool array. Empty list is
		// intentional today; server treats it as a no-op confirming
		// the route works. See Long for context.
		body := map[string]interface{}{"tools": []map[string]string{}}
		resp, err := client.Post(
			"/api/v1/crews/"+crewID+"/integrations/"+args[1]+"/tools/refresh",
			body,
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result map[string]any
		if err := cli.ReadJSON(resp, &result); err != nil {
			// Empty 200 is acceptable here.
			fmt.Printf("Tool bindings refresh requested for %s/%s.\n", args[0], args[1])
			return nil
		}
		return newFormatter().JSON(result)
	},
}

// ==========================================
// Agent binding update (per-binding PATCH)
// ==========================================
//
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
	// Workspace-level subcommands
	integrationCmd.AddCommand(intgListCmd)
	integrationCmd.AddCommand(intgAddCmd)
	integrationCmd.AddCommand(intgRemoveCmd)
	integrationCmd.AddCommand(intgEnableCmd)
	integrationCmd.AddCommand(intgDisableCmd)
	integrationCmd.AddCommand(intgGetCmd)
	integrationCmd.AddCommand(intgTestCmd)
	integrationCmd.AddCommand(intgCrewsOverviewCmd)
	integrationCmd.AddCommand(intgBindCmd)
	integrationCmd.AddCommand(intgUnbindCmd)
	integrationCmd.AddCommand(intgAgentListCmd)
	integrationCmd.AddCommand(intgResolveCmd)

	// Nested verb groups
	integrationCmd.AddCommand(integrationCrewCmd)
	integrationCmd.AddCommand(integrationToolsCmd)
	integrationCmd.AddCommand(integrationAgentCmd)

	integrationCrewCmd.AddCommand(intgCrewListCmd)
	integrationCrewCmd.AddCommand(intgCrewCreateCmd)
	integrationCrewCmd.AddCommand(intgCrewUpdateCmd)
	integrationCrewCmd.AddCommand(intgCrewDeleteCmd)
	integrationCrewCmd.AddCommand(intgCrewTestCmd)

	integrationToolsCmd.AddCommand(intgToolsListCmd)
	integrationToolsCmd.AddCommand(intgToolsEnableCmd)
	integrationToolsCmd.AddCommand(intgToolsDisableCmd)
	integrationToolsCmd.AddCommand(intgToolsRefreshCmd)

	integrationAgentCmd.AddCommand(intgAgentUpdateBindingCmd)

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

	intgUnbindCmd.Flags().String("agent", "", "Agent slug (required)")
	intgUnbindCmd.Flags().String("binding-id", "", "Binding ID to remove (required)")

	intgCrewCreateCmd.Flags().String("name", "", "Integration name (slug, required)")
	intgCrewCreateCmd.Flags().String("display", "", "Display name")
	intgCrewCreateCmd.Flags().String("transport", "streamable-http", "Transport: streamable-http or stdio")
	intgCrewCreateCmd.Flags().String("endpoint", "", "MCP server endpoint URL (streamable-http)")
	intgCrewCreateCmd.Flags().String("command", "", "MCP server command (stdio)")
	intgCrewCreateCmd.Flags().String("icon", "", "Lucide icon name")
	intgCrewCreateCmd.Flags().String("link-workspace-server", "", "Link to a workspace-level integration by ID")

	intgCrewUpdateCmd.Flags().String("display", "", "New display name")
	intgCrewUpdateCmd.Flags().String("transport", "", "New transport: streamable-http or stdio")
	intgCrewUpdateCmd.Flags().String("endpoint", "", "New endpoint URL")
	intgCrewUpdateCmd.Flags().String("command", "", "New stdio command")
	intgCrewUpdateCmd.Flags().String("icon", "", "New Lucide icon name")
	intgCrewUpdateCmd.Flags().Bool("enabled", true, "Set enabled state")

	intgCrewDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	intgAgentUpdateBindingCmd.Flags().String("credential", "", "Credential name (empty string clears the binding)")
	intgAgentUpdateBindingCmd.Flags().String("cred-type", "", "Credential type: bearer, api_key, basic")
	intgAgentUpdateBindingCmd.Flags().String("cred-header", "", "Custom header for api_key type")
	intgAgentUpdateBindingCmd.Flags().String("env-var-name", "", "Environment variable name (empty clears it)")
	intgAgentUpdateBindingCmd.Flags().Bool("enabled", true, "Set enabled state")
}
