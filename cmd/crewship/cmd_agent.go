package main

import (
	"fmt"
	"os"
	"strings"

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
	Use:   "get <slug-or-id>",
	Short: "Show agent details",
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

var agentCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}

		body := map[string]interface{}{"name": name}

		if v, _ := flags.GetString("slug"); v != "" {
			body["slug"] = v
		}
		if v, _ := flags.GetString("role"); v != "" {
			body["agent_role"] = v
		}
		if v, _ := flags.GetString("role-title"); v != "" {
			body["role_title"] = v
		}
		if v, _ := flags.GetString("cli-adapter"); v != "" {
			body["cli_adapter"] = v
		}
		if v, _ := flags.GetString("tool-profile"); v != "" {
			body["tool_profile"] = v
		}
		if v, _ := flags.GetString("lead-mode"); v != "" {
			body["lead_mode"] = v
		}
		if v, _ := flags.GetString("llm-provider"); v != "" {
			body["llm_provider"] = v
		}
		if v, _ := flags.GetString("llm-model"); v != "" {
			body["llm_model"] = v
		}
		if v, _ := flags.GetInt("timeout"); v > 0 {
			body["timeout_seconds"] = v
		}
		if v, _ := flags.GetBool("memory"); v {
			body["memory_enabled"] = true
		}

		// System prompt: inline or @file
		if v, _ := flags.GetString("system-prompt"); v != "" {
			if strings.HasPrefix(v, "@") {
				data, err := os.ReadFile(v[1:])
				if err != nil {
					return fmt.Errorf("read system prompt file: %w", err)
				}
				body["system_prompt"] = string(data)
			} else {
				body["system_prompt"] = v
			}
		}

		// Resolve crew slug to ID
		if v, _ := flags.GetString("crew"); v != "" {
			client := newAPIClient()
			crewID, err := resolveCrewID(client, v)
			if err != nil {
				return err
			}
			body["crew_id"] = crewID
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/agents", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Agent created: %s (%s)", created.Slug, created.ID))
		return nil
	},
}

var agentUpdateCmd = &cobra.Command{
	Use:   "update <slug-or-id>",
	Short: "Update an agent",
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

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("role") {
			v, _ := flags.GetString("role")
			body["agent_role"] = v
		}
		if flags.Changed("role-title") {
			v, _ := flags.GetString("role-title")
			body["role_title"] = v
		}
		if flags.Changed("cli-adapter") {
			v, _ := flags.GetString("cli-adapter")
			body["cli_adapter"] = v
		}
		if flags.Changed("tool-profile") {
			v, _ := flags.GetString("tool-profile")
			body["tool_profile"] = v
		}
		if flags.Changed("lead-mode") {
			v, _ := flags.GetString("lead-mode")
			body["lead_mode"] = v
		}
		if flags.Changed("llm-provider") {
			v, _ := flags.GetString("llm-provider")
			body["llm_provider"] = v
		}
		if flags.Changed("llm-model") {
			v, _ := flags.GetString("llm-model")
			body["llm_model"] = v
		}
		if flags.Changed("timeout") {
			v, _ := flags.GetInt("timeout")
			body["timeout_seconds"] = v
		}
		if flags.Changed("memory") {
			v, _ := flags.GetBool("memory")
			body["memory_enabled"] = v
		}
		if flags.Changed("system-prompt") {
			v, _ := flags.GetString("system-prompt")
			if strings.HasPrefix(v, "@") {
				data, err := os.ReadFile(v[1:])
				if err != nil {
					return fmt.Errorf("read system prompt file: %w", err)
				}
				body["system_prompt"] = string(data)
			} else {
				body["system_prompt"] = v
			}
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		resp, err := client.Patch("/api/v1/agents/"+agentID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Agent updated successfully.")
		return nil
	},
}

var agentDeleteCmd = &cobra.Command{
	Use:   "delete <slug-or-id>",
	Short: "Delete an agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete agent %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/agents/" + agentID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Agent deleted.")
		return nil
	},
}

var agentRunsCmd = &cobra.Command{
	Use:   "runs <slug-or-id>",
	Short: "List runs for an agent",
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

		resp, err := client.Get("/api/v1/agents/" + agentID + "/runs")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var runs []struct {
			ID          string  `json:"id"`
			Status      string  `json:"status"`
			TriggerType string  `json:"trigger_type"`
			CreatedAt   string  `json:"created_at"`
			FinishedAt  *string `json:"finished_at"`
		}
		if err := cli.ReadJSON(resp, &runs); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "STATUS", "TRIGGER", "CREATED", "FINISHED"}
		var rows [][]string
		for _, r := range runs {
			finished := "-"
			if r.FinishedAt != nil {
				finished = *r.FinishedAt
			}
			rows = append(rows, []string{r.ID, r.Status, r.TriggerType, r.CreatedAt, finished})
		}
		return f.Auto(runs, headers, rows)
	},
}

var agentStopCmd = &cobra.Command{
	Use:   "stop <slug-or-id>",
	Short: "Stop a running agent",
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

		resp, err := client.Post("/api/v1/agents/"+agentID+"/stop", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Agent %s stopped.", args[0]))
		return nil
	},
}

var agentLogsCmd = &cobra.Command{
	Use:   "logs <slug-or-id>",
	Short: "Show agent container logs",
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

		tail, _ := cmd.Flags().GetInt("tail")
		path := "/api/v1/agents/" + agentID + "/logs"
		if tail > 0 {
			path += fmt.Sprintf("?tail=%d", tail)
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result map[string]interface{}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(result)
		}
		if logs, ok := result["logs"].(string); ok {
			fmt.Print(logs)
		} else {
			fmt.Println("No logs available.")
		}
		return nil
	},
}

var agentDebugCmd = &cobra.Command{
	Use:   "debug <slug-or-id>",
	Short: "Show agent debug info (container state, env, crewshipd status)",
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

		resp, err := client.Get("/api/v1/agents/" + agentID + "/debug")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result map[string]interface{}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		return f.JSON(result)
	},
}

func init() {
	agentListCmd.Flags().String("crew", "", "Filter by crew slug or ID")

	agentCreateCmd.Flags().String("name", "", "Agent name (required)")
	agentCreateCmd.Flags().String("slug", "", "Agent slug (auto-generated from name if empty)")
	agentCreateCmd.Flags().String("crew", "", "Crew slug or ID")
	agentCreateCmd.Flags().String("role", "AGENT", "Agent role: AGENT|LEAD|COORDINATOR")
	agentCreateCmd.Flags().String("role-title", "", "Human-readable role title")
	agentCreateCmd.Flags().String("cli-adapter", "CLAUDE_CODE", "CLI adapter: CLAUDE_CODE|CODEX_CLI|GEMINI_CLI|OPENCODE")
	agentCreateCmd.Flags().String("system-prompt", "", "System prompt text or @file.txt")
	agentCreateCmd.Flags().String("tool-profile", "CODING", "Tool profile: MINIMAL|CODING|MESSAGING|FULL")
	agentCreateCmd.Flags().String("llm-provider", "", "LLM provider: ANTHROPIC|OPENAI|GOOGLE")
	agentCreateCmd.Flags().String("llm-model", "", "LLM model (e.g., claude-haiku-4-5)")
	agentCreateCmd.Flags().Bool("memory", false, "Enable memory")
	agentCreateCmd.Flags().String("lead-mode", "", "Lead mode: active|passive")
	agentCreateCmd.Flags().Int("timeout", 0, "Timeout in seconds")

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
