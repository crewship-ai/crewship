package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

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

var missionCloneCmd = &cobra.Command{
	Use:   "clone <mission-id>",
	Short: "Clone a mission with all its tasks",
	Args:  cobra.ExactArgs(1),
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

		resp, err := client.Post(
			fmt.Sprintf("/api/v1/crews/%s/missions/%s/clone", crewID, missionID),
			body,
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Mission cloned: %s (%s)", result.ID, result.Title))
		return nil
	},
}
