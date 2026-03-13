package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var missionCmd = &cobra.Command{
	Use:   "mission",
	Short: "Manage missions",
}

var missionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all missions",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		path := "/api/v1/missions"
		if crewFilter, _ := cmd.Flags().GetString("crew"); crewFilter != "" {
			crewID, err := resolveCrewID(client, crewFilter)
			if err != nil {
				return err
			}
			path = "/api/v1/crews/" + crewID + "/missions"
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var missions []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Status    string `json:"status"`
			LeadSlug  string `json:"lead_agent_slug"`
			CreatedAt string `json:"created_at"`
			TaskStats *struct {
				Total     int `json:"total"`
				Completed int `json:"completed"`
			} `json:"task_stats"`
		}
		if err := cli.ReadJSON(resp, &missions); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "TITLE", "STATUS", "LEAD", "TASKS", "CREATED"}
		var rows [][]string
		for _, m := range missions {
			tasks := "-"
			if m.TaskStats != nil {
				tasks = fmt.Sprintf("%d/%d", m.TaskStats.Completed, m.TaskStats.Total)
			}
			title := m.Title
			if len(title) > 50 {
				title = title[:47] + "..."
			}
			rows = append(rows, []string{m.ID[:12], title, m.Status, m.LeadSlug, tasks, m.CreatedAt})
		}
		return f.Auto(missions, headers, rows)
	},
}

var missionGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show mission details with tasks",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, fullMissionID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + crewID + "/missions/" + fullMissionID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var mission struct {
			ID          string  `json:"id"`
			Title       string  `json:"title"`
			Description *string `json:"description"`
			Status      string  `json:"status"`
			LeadName    string  `json:"lead_agent_name"`
			LeadSlug    string  `json:"lead_agent_slug"`
			CreatedAt   string  `json:"created_at"`
			CompletedAt *string `json:"completed_at"`
			Tasks       []struct {
				Title     string  `json:"title"`
				Status    string  `json:"status"`
				AgentSlug *string `json:"agent_slug"`
				TaskOrder int     `json:"task_order"`
			} `json:"tasks"`
		}
		if err := cli.ReadJSON(resp, &mission); err != nil {
			return err
		}

		f := newFormatter()
		desc := "-"
		if mission.Description != nil {
			desc = *mission.Description
		}
		completed := "-"
		if mission.CompletedAt != nil {
			completed = *mission.CompletedAt
		}

		pairs := [][]string{
			{"Title", mission.Title},
			{"ID", mission.ID},
			{"Status", mission.Status},
			{"Lead", fmt.Sprintf("%s (%s)", mission.LeadName, mission.LeadSlug)},
			{"Description", desc},
			{"Created", mission.CreatedAt},
			{"Completed", completed},
		}
		f.AutoDetail(mission, pairs)

		if len(mission.Tasks) > 0 && f.Format == "table" {
			fmt.Printf("\n%sTASKS (%d):%s\n", cli.Bold, len(mission.Tasks), cli.Reset)
			headers := []string{"#", "TITLE", "STATUS", "AGENT"}
			var rows [][]string
			for _, t := range mission.Tasks {
				agent := "-"
				if t.AgentSlug != nil {
					agent = *t.AgentSlug
				}
				title := t.Title
				if len(title) > 50 {
					title = title[:47] + "..."
				}
				rows = append(rows, []string{fmt.Sprintf("%d", t.TaskOrder), title, t.Status, agent})
			}
			w := cli.NewFormatter("table")
			w.Table(headers, rows)
		}

		return nil
	},
}

var missionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a mission",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		title, _ := cmd.Flags().GetString("title")
		description, _ := cmd.Flags().GetString("description")
		crewSlug, _ := cmd.Flags().GetString("crew")
		leadSlug, _ := cmd.Flags().GetString("lead")

		if title == "" {
			return fmt.Errorf("--title is required")
		}
		if crewSlug == "" {
			return fmt.Errorf("--crew is required")
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		var leadAgentID string
		if leadSlug != "" {
			leadAgentID, err = resolveAgentID(client, leadSlug)
			if err != nil {
				return err
			}
		} else {
			leadAgentID, err = findLeadAgent(client, crewID)
			if err != nil {
				return err
			}
		}

		body := map[string]interface{}{
			"title":         title,
			"lead_agent_id": leadAgentID,
		}
		if description != "" {
			body["description"] = description
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/missions", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Mission created: %s (%s)", created.Title, created.ID))
		return nil
	},
}

var missionUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a mission",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, fullID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		if cmd.Flags().Changed("title") {
			v, _ := cmd.Flags().GetString("title")
			body["title"] = v
		}
		if cmd.Flags().Changed("description") {
			v, _ := cmd.Flags().GetString("description")
			body["description"] = v
		}
		if cmd.Flags().Changed("status") {
			v, _ := cmd.Flags().GetString("status")
			body["status"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		resp, err := client.Patch("/api/v1/crews/"+crewID+"/missions/"+fullID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Mission updated.")
		return nil
	},
}

var missionDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a mission",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete mission %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, fullID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/crews/" + crewID + "/missions/" + fullID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Mission deleted.")
		return nil
	},
}

func resolveMission(client *cli.Client, missionID string) (crewID, fullMissionID string, err error) {
	listResp, err := client.Get("/api/v1/missions")
	if err != nil {
		return "", "", err
	}
	if err := cli.CheckError(listResp); err != nil {
		return "", "", err
	}

	var missions []struct {
		ID     string `json:"id"`
		CrewID string `json:"crew_id"`
	}
	if err := cli.ReadJSON(listResp, &missions); err != nil {
		return "", "", err
	}

	for _, m := range missions {
		if m.ID == missionID || (len(missionID) >= 8 && len(m.ID) >= len(missionID) && m.ID[:len(missionID)] == missionID) {
			return m.CrewID, m.ID, nil
		}
	}
	return "", "", fmt.Errorf("mission not found: %s", missionID)
}

func findLeadAgent(client *cli.Client, crewID string) (string, error) {
	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var agents []struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		Role   string `json:"agent_role"`
		CrewID string `json:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return "", err
	}

	for _, a := range agents {
		if a.CrewID == crewID && a.Role == "LEAD" {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("no LEAD agent found in crew; use --lead to specify one")
}

var missionStartCmd = &cobra.Command{
	Use:   "start <id>",
	Short: "Start a PLANNING mission (kicks off the MissionEngine DAG loop)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, fullID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/missions/"+fullID+"/start", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Mission %s started — MissionEngine DAG loop is running.", args[0]))
		return nil
	},
}

var missionResumeCmd = &cobra.Command{
	Use:   "resume <id>",
	Short: "Resume a FAILED mission from the point of failure (resets only failed tasks + dependents)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, fullID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/missions/"+fullID+"/resume", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		resetCount := 0
		if v, ok := result["reset_tasks"]; ok {
			if f, ok := v.(float64); ok {
				resetCount = int(f)
			}
		}
		cli.PrintSuccess(fmt.Sprintf("Mission %s resumed — %d task(s) reset, DAG engine running.", args[0], resetCount))
		return nil
	},
}

var missionAddTaskCmd = &cobra.Command{
	Use:   "add-task <missionId>",
	Short: "Add a task to a mission",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		title, _ := cmd.Flags().GetString("title")
		if title == "" {
			return fmt.Errorf("--title is required")
		}

		client := newAPIClient()
		crewID, fullMissionID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{
			"title": title,
		}

		if desc, _ := cmd.Flags().GetString("description"); desc != "" {
			body["description"] = desc
		}
		if order, _ := cmd.Flags().GetInt("order"); order > 0 {
			body["task_order"] = order
		}
		if agentSlug, _ := cmd.Flags().GetString("agent"); agentSlug != "" {
			agentID, err := resolveAgentID(client, agentSlug)
			if err != nil {
				return err
			}
			body["assigned_agent_id"] = agentID
		}
		if deps, _ := cmd.Flags().GetString("depends-on"); deps != "" {
			var depIDs []string
			for _, d := range strings.Split(deps, ",") {
				d = strings.TrimSpace(d)
				if d != "" {
					depIDs = append(depIDs, d)
				}
			}
			if len(depIDs) > 0 {
				body["depends_on"] = depIDs
			}
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/missions/"+fullMissionID+"/tasks", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Task added: %s (%s)", created.Title, created.ID))
		return nil
	},
}

func init() {
	missionListCmd.Flags().String("crew", "", "Filter by crew slug or ID")

	missionCreateCmd.Flags().String("title", "", "Mission title (required)")
	missionCreateCmd.Flags().String("description", "", "Mission description")
	missionCreateCmd.Flags().String("crew", "", "Crew slug or ID (required)")
	missionCreateCmd.Flags().String("lead", "", "Lead agent slug or ID (auto-detected if omitted)")

	missionUpdateCmd.Flags().String("title", "", "Mission title")
	missionUpdateCmd.Flags().String("description", "", "Mission description")
	missionUpdateCmd.Flags().String("status", "", "Status: PLANNING|IN_PROGRESS|COMPLETED|FAILED")

	missionDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	missionAddTaskCmd.Flags().String("title", "", "Task title (required)")
	missionAddTaskCmd.Flags().String("description", "", "Task description")
	missionAddTaskCmd.Flags().String("agent", "", "Agent slug or ID to assign")
	missionAddTaskCmd.Flags().Int("order", 0, "Task order (1-based)")
	missionAddTaskCmd.Flags().String("depends-on", "", "Comma-separated task IDs this task depends on")

	missionCmd.AddCommand(missionListCmd)
	missionCmd.AddCommand(missionGetCmd)
	missionCmd.AddCommand(missionCreateCmd)
	missionCmd.AddCommand(missionUpdateCmd)
	missionCmd.AddCommand(missionDeleteCmd)
	missionCmd.AddCommand(missionStartCmd)
	missionCmd.AddCommand(missionResumeCmd)
	missionCmd.AddCommand(missionAddTaskCmd)
}
