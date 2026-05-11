package main

// Agent runtime introspection + control commands: runs, stop, logs,
// debug, skills, credentials. Extracted from cmd_agent.go.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

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

var agentSkillsCmd = &cobra.Command{
	Use:   "skills <agent>",
	Short: "List skills assigned to an agent",
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

		resp, err := client.Get(fmt.Sprintf("/api/v1/agents/%s/skills", agentID))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var skills []struct {
			ID        string `json:"id"`
			SkillID   string `json:"skill_id"`
			SkillName string `json:"skill_name"`
			Category  string `json:"category"`
			Enabled   bool   `json:"enabled"`
		}
		if err := cli.ReadJSON(resp, &skills); err != nil {
			return err
		}

		if len(skills) == 0 {
			fmt.Println("No skills assigned to this agent.")
			return nil
		}

		f := newFormatter()
		headers := []string{"SKILL ID", "NAME", "CATEGORY", "ENABLED"}
		var rows [][]string
		for _, s := range skills {
			enabled := "yes"
			if !s.Enabled {
				enabled = "no"
			}
			rows = append(rows, []string{s.SkillID[:min(12, len(s.SkillID))], s.SkillName, s.Category, enabled})
		}
		return f.Auto(skills, headers, rows)
	},
}

// agentChatsCmd is a thin alias for the chat-side `crewship chat list`
// command, exposed under `agent chats <slug>` so introspection lives next
// to the other per-agent lookups (runs, skills, credentials). Both call
// the same /agents/{id}/chats endpoint.
var agentChatsCmd = &cobra.Command{
	Use:     "chats <slug-or-id>",
	Aliases: []string{"sessions"},
	Short:   "List recent chats for an agent",
	Long: `Show the agent's recent chat sessions: id, title, status, message
count and when they started. Mirrors the SessionsSidebar shown in the web
UI.

Examples:
  crewship agent chats viktor
  crewship agent chats viktor --format json | jq '.[] | select(.status=="ACTIVE")'`,
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

		var chats []struct {
			ID           string  `json:"id"`
			Title        *string `json:"title"`
			Status       string  `json:"status"`
			MessageCount int     `json:"message_count"`
			StartedAt    string  `json:"started_at"`
			EndedAt      *string `json:"ended_at"`
			Origin       *string `json:"origin"`
		}
		if err := getJSON(client, "/api/v1/agents/"+agentID+"/chats", &chats); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"CHAT ID", "TITLE", "STATUS", "MSGS", "STARTED", "ENDED"}
		var rows [][]string
		for _, c := range chats {
			title := "-"
			if c.Title != nil && *c.Title != "" {
				title = *c.Title
			}
			ended := "-"
			if c.EndedAt != nil && *c.EndedAt != "" {
				ended = *c.EndedAt
			}
			rows = append(rows, []string{
				c.ID, title, c.Status,
				fmt.Sprintf("%d", c.MessageCount),
				c.StartedAt, ended,
			})
		}
		return f.Auto(chats, headers, rows)
	},
}

var agentCredentialsCmd = &cobra.Command{
	Use:   "credentials <agent>",
	Short: "List credentials assigned to an agent",
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

		resp, err := client.Get(fmt.Sprintf("/api/v1/agents/%s/credentials", agentID))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var creds []struct {
			ID             string `json:"id"`
			CredentialID   string `json:"credential_id"`
			CredentialName string `json:"credential_name"`
			Provider       string `json:"provider"`
			Type           string `json:"type"`
			EnvVarName     string `json:"env_var_name"`
		}
		if err := cli.ReadJSON(resp, &creds); err != nil {
			return err
		}

		if len(creds) == 0 {
			fmt.Println("No credentials assigned to this agent.")
			return nil
		}

		f := newFormatter()
		headers := []string{"ID", "NAME", "PROVIDER", "TYPE", "ENV VAR"}
		var rows [][]string
		for _, c := range creds {
			rows = append(rows, []string{c.ID[:min(12, len(c.ID))], c.CredentialName, c.Provider, c.Type, c.EnvVarName})
		}
		return f.Auto(creds, headers, rows)
	},
}

func init() {
	// Registered here rather than in cmd_agent.go so the introspect file
	// stays self-contained — the rest of the cmd_agent_*.go siblings do
	// the same.
	agentCmd.AddCommand(agentChatsCmd)
}
