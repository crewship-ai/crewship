package main

import (
	"fmt"

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
				ID        string  `json:"id"`
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
			headers := []string{"#", "TASK ID", "TITLE", "STATUS", "AGENT"}
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
				rows = append(rows, []string{fmt.Sprintf("%d", t.TaskOrder), t.ID, title, t.Status, agent})
			}
			w := cli.NewFormatter("table")
			w.Table(headers, rows)
		}

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
	missionRestartCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	missionAddTaskCmd.Flags().String("title", "", "Task title (required)")
	missionAddTaskCmd.Flags().String("description", "", "Task description")
	missionAddTaskCmd.Flags().String("agent", "", "Agent slug or ID to assign")
	missionAddTaskCmd.Flags().Int("order", 0, "Task order (1-based)")
	missionAddTaskCmd.Flags().String("depends-on", "", "Comma-separated task IDs this task depends on")

	missionTaskUpdateCmd.Flags().String("title", "", "New task title")
	missionTaskUpdateCmd.Flags().String("description", "", "New task description")
	missionTaskUpdateCmd.Flags().String("status", "", "New status: PENDING|IN_PROGRESS|COMPLETED|FAILED")
	missionTaskUpdateCmd.Flags().String("assigned-agent", "", "Agent ID to assign")

	missionCloneCmd.Flags().String("title", "", "Override title for cloned mission")

	missionCmd.AddCommand(missionListCmd)
	missionCmd.AddCommand(missionGetCmd)
	missionCmd.AddCommand(missionCreateCmd)
	missionCmd.AddCommand(missionUpdateCmd)
	missionCmd.AddCommand(missionDeleteCmd)
	missionCmd.AddCommand(missionStartCmd)
	missionCmd.AddCommand(missionResumeCmd)
	missionCmd.AddCommand(missionRestartCmd)
	missionCmd.AddCommand(missionAddTaskCmd)
	missionCmd.AddCommand(missionTaskUpdateCmd)
	missionCmd.AddCommand(missionCloneCmd)
}
