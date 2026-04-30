package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents",
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all agents in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		path := "/api/v1/agents"
		if crewFilter, _ := cmd.Flags().GetString("crew"); crewFilter != "" {
			crewID, err := resolveCrewID(client, crewFilter)
			if err != nil {
				return err
			}
			path += "?crew_id=" + crewID
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var agents []agentListItem
		if err := cli.ReadJSON(resp, &agents); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "ROLE", "CREW", "STATUS", "ADAPTER", "MEMORY"}
		var rows [][]string
		for _, a := range agents {
			crewName := "-"
			if a.Crew != nil {
				crewName = a.Crew.Slug
			}
			mem := "off"
			if a.MemoryEnabled {
				mem = "on"
			}
			rows = append(rows, []string{a.Slug, a.AgentRole, crewName, a.Status, a.CLIAdapter, mem})
		}
		return f.Auto(agents, headers, rows)
	},
}

var agentGetCmd = &cobra.Command{
	Use:               "get <slug-or-id>",
	Short:             "Show agent details",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
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

		resp, err := client.Get("/api/v1/agents/" + agentID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var agent agentDetailResponse
		if err := cli.ReadJSON(resp, &agent); err != nil {
			return err
		}

		f := newFormatter()
		crewName := "-"
		if agent.Crew != nil {
			crewName = agent.Crew.Slug
		}
		mem := "off"
		if agent.MemoryEnabled {
			mem = "on"
		}
		pairs := [][]string{
			{"Name", agent.Name},
			{"Slug", agent.Slug},
			{"ID", agent.ID},
			{"Role", agent.AgentRole},
			{"Crew", crewName},
			{"Status", agent.Status},
			{"CLI Adapter", agent.CLIAdapter},
			{"Tool Profile", agent.ToolProfile},
			{"Memory", mem},
			{"Timeout", fmt.Sprintf("%ds", agent.TimeoutSeconds)},
			{"Created", agent.CreatedAt},
			{"Skills", fmt.Sprintf("%d", agent.Count.Skills)},
			{"Credentials", fmt.Sprintf("%d", agent.Count.Credentials)},
		}
		if agent.RoleTitle != nil {
			pairs = append([][]string{pairs[0], pairs[1], pairs[2], {"Role Title", *agent.RoleTitle}}, pairs[3:]...)
		}
		return f.AutoDetail(agent, pairs)
	},
}

func init() {
	agentListCmd.Flags().String("crew", "", "Filter by crew slug or ID")

	agentCreateCmd.Flags().String("name", "", "Agent name (required)")
	agentCreateCmd.Flags().String("slug", "", "Agent slug (auto-generated from name if empty)")
	agentCreateCmd.Flags().String("crew", "", "Crew slug or ID")
	agentCreateCmd.Flags().String("role", "AGENT", "Agent role: AGENT|LEAD|COORDINATOR (deprecated)")
	agentCreateCmd.Flags().String("role-title", "", "Human-readable role title")
	agentCreateCmd.Flags().String("cli-adapter", "CLAUDE_CODE", "CLI adapter: CLAUDE_CODE|CODEX_CLI|GEMINI_CLI|OPENCODE")
	agentCreateCmd.Flags().String("system-prompt", "", "System prompt text or @file.txt")
	agentCreateCmd.Flags().String("tool-profile", "CODING", "Tool profile: MINIMAL|CODING|MESSAGING|FULL")
	agentCreateCmd.Flags().String("llm-provider", "", "LLM provider: ANTHROPIC|OPENAI|GOOGLE")
	agentCreateCmd.Flags().String("llm-model", "", "LLM model (e.g., claude-haiku-4-5)")
	agentCreateCmd.Flags().Bool("memory", false, "Enable memory")
	agentCreateCmd.Flags().String("lead-mode", "", "Lead mode: active|passive")
	agentCreateCmd.Flags().Int("timeout", 0, "Timeout in seconds")
	agentCreateCmd.Flags().String("avatar-seed", "", "Avatar seed (defaults to agent name)")
	agentCreateCmd.Flags().String("avatar-style", "", "Avatar style: bottts-neutral|adventurer|fun-emoji|pixel-art|micah|notionists|thumbs|lorelei|big-smile|avataaars")

	agentUpdateCmd.Flags().String("name", "", "Agent name")
	agentUpdateCmd.Flags().String("role", "", "Agent role")
	agentUpdateCmd.Flags().String("role-title", "", "Human-readable role title")
	agentUpdateCmd.Flags().String("cli-adapter", "", "CLI adapter")
	agentUpdateCmd.Flags().String("system-prompt", "", "System prompt text or @file.txt")
	agentUpdateCmd.Flags().String("tool-profile", "", "Tool profile")
	agentUpdateCmd.Flags().String("llm-provider", "", "LLM provider: ANTHROPIC|OPENAI|GOOGLE")
	agentUpdateCmd.Flags().String("llm-model", "", "LLM model")
	agentUpdateCmd.Flags().Bool("memory", false, "Enable memory")
	agentUpdateCmd.Flags().String("lead-mode", "", "Lead mode")
	agentUpdateCmd.Flags().Int("timeout", 0, "Timeout in seconds")
	agentUpdateCmd.Flags().String("avatar-seed", "", "Avatar seed")
	agentUpdateCmd.Flags().String("avatar-style", "", "Avatar style")

	agentDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	agentLogsCmd.Flags().Int("tail", 100, "Number of log lines to show")

	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentGetCmd)
	agentCmd.AddCommand(agentCreateCmd)
	agentCmd.AddCommand(agentUpdateCmd)
	agentCmd.AddCommand(agentDeleteCmd)
	agentCmd.AddCommand(agentRunsCmd)
	agentCmd.AddCommand(agentStopCmd)
	agentCmd.AddCommand(agentLogsCmd)
	agentCmd.AddCommand(agentDebugCmd)
	agentCmd.AddCommand(agentSkillsCmd)
	agentCmd.AddCommand(agentCredentialsCmd)
}

// Resolver helpers and shared types

type agentListItem struct {
	ID            string          `json:"id"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	AgentRole     string          `json:"agent_role"`
	Status        string          `json:"status"`
	CLIAdapter    string          `json:"cli_adapter"`
	MemoryEnabled bool            `json:"memory_enabled"`
	Crew          *agentCrewShort `json:"crew"`
}

type agentCrewShort struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type agentDetailResponse struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Slug           string          `json:"slug"`
	AgentRole      string          `json:"agent_role"`
	RoleTitle      *string         `json:"role_title"`
	Status         string          `json:"status"`
	CLIAdapter     string          `json:"cli_adapter"`
	ToolProfile    string          `json:"tool_profile"`
	MemoryEnabled  bool            `json:"memory_enabled"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	CreatedAt      string          `json:"created_at"`
	Crew           *agentCrewShort `json:"crew"`
	Count          struct {
		Skills      int `json:"skills"`
		Credentials int `json:"credentials"`
	} `json:"_count"`
}

func resolveAgentID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		return slugOrID, nil
	}

	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return "", fmt.Errorf("resolve agent: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var agents []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return "", err
	}

	for _, a := range agents {
		if a.Slug == slugOrID {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("agent not found: %s", slugOrID)
}

func resolveCrewID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		return slugOrID, nil
	}

	resp, err := client.Get("/api/v1/crews")
	if err != nil {
		return "", fmt.Errorf("resolve crew: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var crews []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &crews); err != nil {
		return "", err
	}

	for _, c := range crews {
		if c.Slug == slugOrID {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("crew not found: %s", slugOrID)
}

func looksLikeCUID(s string) bool {
	if len(s) < 20 || s[0] != 'c' {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
