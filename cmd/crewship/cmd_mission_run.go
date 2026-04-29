package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

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
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decode response: %w", err)
		}
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

var missionRestartCmd = &cobra.Command{
	Use:   "restart <id>",
	Short: "Restart a mission from the beginning (resets all tasks)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Restart mission %q from the beginning?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, fullID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/missions/"+fullID+"/restart", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Mission %s restarted — all tasks reset, DAG engine running.", args[0]))
		return nil
	},
}

var missionTaskUpdateCmd = &cobra.Command{
	Use:   "task-update <mission-id> <task-id>",
	Short: "Update a mission task",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, missionID, err := resolveMission(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		if v, _ := cmd.Flags().GetString("title"); v != "" {
			body["title"] = v
		}
		if v, _ := cmd.Flags().GetString("description"); v != "" {
			body["description"] = v
		}
		if v, _ := cmd.Flags().GetString("status"); v != "" {
			body["status"] = strings.ToUpper(v)
		}
		if v, _ := cmd.Flags().GetString("assigned-agent"); v != "" {
			body["assigned_agent_id"] = v
		}
		if len(body) == 0 {
			return fmt.Errorf("no updates specified (use --title, --description, --status, or --assigned-agent)")
		}

		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/crews/%s/missions/%s/tasks/%s", crewID, missionID, args[1]),
			body,
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Task updated.")
		return nil
	},
}
