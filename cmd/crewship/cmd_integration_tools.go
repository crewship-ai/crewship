package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

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

func registerIntegrationToolsSubcommands() {
	integrationToolsCmd.AddCommand(intgToolsListCmd)
	integrationToolsCmd.AddCommand(intgToolsEnableCmd)
	integrationToolsCmd.AddCommand(intgToolsDisableCmd)
	integrationToolsCmd.AddCommand(intgToolsRefreshCmd)
}
