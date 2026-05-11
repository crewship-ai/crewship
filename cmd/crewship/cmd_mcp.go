package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// mcpCmd is the top-level MCP root. Existing `crewship crew mcp` and
// `crewship agent mcp` continue to live under their owning nouns
// (further down in this file); this new root exposes the audit log and
// the registry surfaces, neither of which fits cleanly under crew/agent.
//
// Aliases: also matches `mcp-calls` (kept around as a top-level shortcut
// for `mcp audit list`). The legacy `mcp-calls` command is registered in
// cmd_admin_extras.go and we don't touch it here — both reach the same
// /api/v1/mcp-tool-calls endpoint.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP registry, audit log, and tool-call observability",
}

var mcpAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Inspect MCP tool-call audit data",
}

var mcpRegistryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Browse and sync the local MCP server registry cache",
}

// mcpAuditListCmd renders recent MCP tool invocations across the
// workspace. Same data as `crewship mcp-calls` but namespaced under the
// new mcp root so it lives next to the registry commands. Default page
// size matches the legacy alias (50) for parity.
var mcpAuditListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent MCP tool calls (audit log)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		limit, _ := cmd.Flags().GetInt("limit")
		since, _ := cmd.Flags().GetString("since")
		q := url.Values{}
		if limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", limit))
		}
		if since != "" {
			q.Set("since", since)
		}
		path := "/api/v1/mcp-tool-calls"
		if encoded := q.Encode(); encoded != "" {
			path += "?" + encoded
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body any
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		return newFormatter().JSON(body)
	},
}

// mcpRegistryListCmd shows the cached registry — public MCP servers
// the local cache has synced from the official registry.
var mcpRegistryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List MCP registry entries (cached)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		// Registry is workspace-agnostic; clear ws header to skip the
		// wsCtx middleware lookup on the server.
		client.WorkspaceID = ""

		q := url.Values{}
		if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", limit))
		}
		if tier, _ := cmd.Flags().GetString("trust-tier"); tier != "" {
			q.Set("trust_tier", tier)
		}
		if featured, _ := cmd.Flags().GetBool("featured"); featured {
			q.Set("featured", "true")
		}
		path := "/api/v1/mcp-registry"
		if encoded := q.Encode(); encoded != "" {
			path += "?" + encoded
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			Servers []struct {
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
				Category    string `json:"category"`
				Transport   string `json:"transport"`
				TrustTier   string `json:"trust_tier"`
				IsFeatured  bool   `json:"is_featured"`
				PackageName string `json:"package_name"`
			} `json:"servers"`
			Total int `json:"total"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"NAME", "DISPLAY", "CATEGORY", "TRANSPORT", "TRUST", "FEATURED", "PACKAGE"}
		var rows [][]string
		for _, s := range result.Servers {
			rows = append(rows, []string{
				s.Name, s.DisplayName, s.Category, s.Transport,
				s.TrustTier, yesNo(s.IsFeatured), s.PackageName,
			})
		}
		if err := f.Auto(result, headers, rows); err != nil {
			return err
		}
		if len(rows) > 0 && f.Format == "" {
			fmt.Fprintf(os.Stderr, "Total: %d\n", result.Total)
		}
		return nil
	},
}

var mcpRegistrySearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the MCP registry by name / description / category",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		client.WorkspaceID = ""

		q := url.Values{}
		q.Set("q", args[0])
		if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", limit))
		}
		if tier, _ := cmd.Flags().GetString("trust-tier"); tier != "" {
			q.Set("trust_tier", tier)
		}
		resp, err := client.Get("/api/v1/mcp-registry/search?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body any
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		return newFormatter().JSON(body)
	},
}

// mcpRegistrySyncCmd triggers a manual sync of the registry cache from
// the upstream feed. Admin-only on the server; cooldown is 1h, so a
// 429 response is normal if someone synced recently.
var mcpRegistrySyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Trigger a manual sync of the MCP registry cache (admin)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/mcp-registry/sync", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.Message != "" {
			cli.PrintSuccess(out.Message)
		} else {
			cli.PrintSuccess("MCP registry sync triggered.")
		}
		return nil
	},
}

// mcpConfig is the validated structure of an MCP JSON config.
type mcpConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// validateAndNormalizeMCPJSON validates that value is valid MCP JSON with a
// "mcpServers" key, and returns a compact-printed version. If value is empty
// it returns ("", 0, nil).
func validateAndNormalizeMCPJSON(value string) (string, int, error) {
	if value == "" {
		return "", 0, nil
	}
	var check mcpConfig
	if err := json.Unmarshal([]byte(value), &check); err != nil {
		return "", 0, fmt.Errorf("invalid MCP JSON: %w", err)
	}
	if check.MCPServers == nil {
		return "", 0, fmt.Errorf("JSON must contain a \"mcpServers\" object")
	}
	pretty, _ := json.Marshal(check)
	return string(pretty), len(check.MCPServers), nil
}

// ==========================================
// crewship crew mcp <slug> [--set|--set-file]
// ==========================================

var crewMCPCmd = &cobra.Command{
	Use:   "mcp <crew-slug>",
	Short: "Show or set MCP server configuration for a crew",
	Example: `  # Show current config
  crewship crew mcp engineering

  # Set from inline JSON
  crewship crew mcp engineering --set '{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"],"env":{"GITHUB_TOKEN":"${GITHUB_TOKEN}"}}}}'

  # Set from file
  crewship crew mcp engineering --set-file .mcp.json

  # Clear config
  crewship crew mcp engineering --set ''`,
	Args: cobra.ExactArgs(1),
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

		setJSON, _ := cmd.Flags().GetString("set")
		setFile, _ := cmd.Flags().GetString("set-file")

		if cmd.Flags().Changed("set") && setFile != "" {
			return fmt.Errorf("--set and --set-file are mutually exclusive")
		}

		// SET mode
		if cmd.Flags().Changed("set") || setFile != "" {
			value := setJSON
			if setFile != "" {
				data, err := os.ReadFile(setFile)
				if err != nil {
					return fmt.Errorf("read file %s: %w", setFile, err)
				}
				value = string(data)
			}

			// Validate JSON if non-empty
			normalized, serverCount, err := validateAndNormalizeMCPJSON(value)
			if err != nil {
				return err
			}
			value = normalized

			body := map[string]interface{}{"mcp_config_json": value}
			if value == "" {
				body["mcp_config_json"] = nil
			}

			resp, err := client.Patch("/api/v1/crews/"+crewID, body)
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}

			if value == "" {
				fmt.Printf("Crew %s: MCP config cleared.\n", args[0])
			} else {
				fmt.Printf("Crew %s: MCP config set (%d servers).\n", args[0], serverCount)
			}
			return nil
		}

		// GET mode
		resp, err := client.Get("/api/v1/crews/" + crewID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var crew struct {
			MCPConfigJSON *string `json:"mcp_config_json"`
		}
		if err := cli.ReadJSON(resp, &crew); err != nil {
			return err
		}

		if crew.MCPConfigJSON == nil || *crew.MCPConfigJSON == "" {
			fmt.Printf("Crew %s: no MCP config set.\n", args[0])
			return nil
		}

		// Pretty print
		var raw interface{}
		if err := json.Unmarshal([]byte(*crew.MCPConfigJSON), &raw); err != nil {
			fmt.Println(*crew.MCPConfigJSON)
			return nil
		}
		pretty, _ := json.MarshalIndent(raw, "", "  ")
		fmt.Println(string(pretty))
		return nil
	},
}

// ==========================================
// crewship agent mcp <slug> [--set|--set-file|--resolved]
// ==========================================

var agentMCPCmd = &cobra.Command{
	Use:   "mcp <agent-slug>",
	Short: "Show or set MCP server configuration for an agent",
	Example: `  # Show agent-specific config
  crewship agent mcp viktor

  # Show effective (merged crew + agent) config
  crewship agent mcp viktor --resolved

  # Set from inline JSON
  crewship agent mcp viktor --set '{"mcpServers":{"jira":{"type":"http","url":"https://mcp.atlassian.com/jira","headers":{"Authorization":"Bearer ${JIRA_TOKEN}"}}}}'

  # Set from file
  crewship agent mcp viktor --set-file agent-mcp.json`,
	Args: cobra.ExactArgs(1),
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

		resolved, _ := cmd.Flags().GetBool("resolved")
		setJSON, _ := cmd.Flags().GetString("set")
		setFile, _ := cmd.Flags().GetString("set-file")

		// Flag conflict validation
		setMode := cmd.Flags().Changed("set") || setFile != ""
		if resolved && setMode {
			return fmt.Errorf("--resolved cannot be combined with --set or --set-file")
		}
		if cmd.Flags().Changed("set") && setFile != "" {
			return fmt.Errorf("--set and --set-file are mutually exclusive")
		}

		// SET mode
		if setMode {
			value := setJSON
			if setFile != "" {
				data, err := os.ReadFile(setFile)
				if err != nil {
					return fmt.Errorf("read file %s: %w", setFile, err)
				}
				value = string(data)
			}

			normalized, serverCount, err := validateAndNormalizeMCPJSON(value)
			if err != nil {
				return err
			}
			value = normalized

			body := map[string]interface{}{"mcp_config_json": value}
			if value == "" {
				body["mcp_config_json"] = nil
			}

			resp, err := client.Patch("/api/v1/agents/"+agentID, body)
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}

			if value == "" {
				fmt.Printf("Agent %s: MCP config cleared.\n", args[0])
			} else {
				fmt.Printf("Agent %s: MCP config set (%d servers).\n", args[0], serverCount)
			}
			return nil
		}

		// RESOLVED mode: show merged crew + agent config
		if resolved {
			resp, err := client.Get("/api/v1/agents/" + agentID)
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var agent struct {
				CrewID        *string `json:"crew_id"`
				MCPConfigJSON *string `json:"mcp_config_json"`
			}
			if err := cli.ReadJSON(resp, &agent); err != nil {
				return err
			}

			crewServers := map[string]json.RawMessage{}
			if agent.CrewID != nil && *agent.CrewID != "" {
				crewResp, err := client.Get("/api/v1/crews/" + *agent.CrewID)
				if err != nil {
					return fmt.Errorf("fetch crew: %w", err)
				}
				if err := cli.CheckError(crewResp); err != nil {
					return err
				}
				var crew struct {
					MCPConfigJSON *string `json:"mcp_config_json"`
				}
				if err := cli.ReadJSON(crewResp, &crew); err != nil {
					return fmt.Errorf("read crew response: %w", err)
				}
				if crew.MCPConfigJSON != nil && *crew.MCPConfigJSON != "" {
					var parsed struct {
						MCPServers map[string]json.RawMessage `json:"mcpServers"`
					}
					if err := json.Unmarshal([]byte(*crew.MCPConfigJSON), &parsed); err != nil {
						return fmt.Errorf("parse crew MCP config: %w", err)
					}
					crewServers = parsed.MCPServers
				}
			}

			agentServers := map[string]json.RawMessage{}
			if agent.MCPConfigJSON != nil && *agent.MCPConfigJSON != "" {
				var parsed struct {
					MCPServers map[string]json.RawMessage `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(*agent.MCPConfigJSON), &parsed); err != nil {
					return fmt.Errorf("malformed agent MCP config: %w", err)
				}
				agentServers = parsed.MCPServers
			}

			// Merge: agent overrides crew
			merged := make(map[string]json.RawMessage)
			for k, v := range crewServers {
				merged[k] = v
			}
			for k, v := range agentServers {
				merged[k] = v
			}

			if len(merged) == 0 {
				fmt.Printf("Agent %s: no MCP servers (crew + agent both empty).\n", args[0])
				return nil
			}

			wrapper := map[string]interface{}{"mcpServers": merged}
			pretty, _ := json.MarshalIndent(wrapper, "", "  ")
			fmt.Println(string(pretty))
			return nil
		}

		// GET mode: show agent-specific only
		resp, err := client.Get("/api/v1/agents/" + agentID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var agent struct {
			MCPConfigJSON *string `json:"mcp_config_json"`
		}
		if err := cli.ReadJSON(resp, &agent); err != nil {
			return err
		}

		if agent.MCPConfigJSON == nil || *agent.MCPConfigJSON == "" {
			fmt.Printf("Agent %s: no agent-specific MCP config.\n", args[0])
			return nil
		}

		var raw interface{}
		if err := json.Unmarshal([]byte(*agent.MCPConfigJSON), &raw); err != nil {
			fmt.Println(*agent.MCPConfigJSON)
			return nil
		}
		pretty, _ := json.MarshalIndent(raw, "", "  ")
		fmt.Println(string(pretty))
		return nil
	},
}

func init() {
	crewMCPCmd.Flags().String("set", "", "Set MCP config from JSON string")
	crewMCPCmd.Flags().String("set-file", "", "Set MCP config from file path")
	crewCmd.AddCommand(crewMCPCmd)

	agentMCPCmd.Flags().String("set", "", "Set MCP config from JSON string")
	agentMCPCmd.Flags().String("set-file", "", "Set MCP config from file path")
	agentMCPCmd.Flags().Bool("resolved", false, "Show effective merged config (crew + agent)")
	agentCmd.AddCommand(agentMCPCmd)

	// New top-level: crewship mcp audit list, mcp registry list/search/sync.
	mcpAuditListCmd.Flags().Int("limit", 50, "Max calls to return")
	mcpAuditListCmd.Flags().String("since", "", "Only calls newer than this ISO-8601 timestamp")

	mcpRegistryListCmd.Flags().Int("limit", 50, "Max registry entries to return (max 200)")
	mcpRegistryListCmd.Flags().String("trust-tier", "", "Filter by trust tier: anthropic|crewship|community")
	mcpRegistryListCmd.Flags().Bool("featured", false, "Only show featured entries")

	mcpRegistrySearchCmd.Flags().Int("limit", 50, "Max search results")
	mcpRegistrySearchCmd.Flags().String("trust-tier", "", "Filter by trust tier: anthropic|crewship|community")

	mcpAuditCmd.AddCommand(mcpAuditListCmd)
	mcpRegistryCmd.AddCommand(mcpRegistryListCmd)
	mcpRegistryCmd.AddCommand(mcpRegistrySearchCmd)
	mcpRegistryCmd.AddCommand(mcpRegistrySyncCmd)

	mcpCmd.AddCommand(mcpAuditCmd)
	mcpCmd.AddCommand(mcpRegistryCmd)

	// Register the new top-level here rather than touching main.go —
	// the parent file is intentionally a flat list of AddCommand calls
	// kept in alphabetical-ish order. The old `integration` "mcp" alias
	// was removed earlier in this wave; this root takes its place.
	rootCmd.AddCommand(mcpCmd)
}
