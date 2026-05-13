package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

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

func registerIntegrationCrewSubcommands() {
	integrationCrewCmd.AddCommand(intgCrewListCmd)
	integrationCrewCmd.AddCommand(intgCrewCreateCmd)
	integrationCrewCmd.AddCommand(intgCrewUpdateCmd)
	integrationCrewCmd.AddCommand(intgCrewDeleteCmd)
	integrationCrewCmd.AddCommand(intgCrewTestCmd)
}

func registerIntegrationCrewFlags() {
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
}
