package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

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
				if crew.MCPConfigJSON != nil {
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
				if json.Unmarshal([]byte(*agent.MCPConfigJSON), &parsed) == nil {
					agentServers = parsed.MCPServers
				}
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
}
