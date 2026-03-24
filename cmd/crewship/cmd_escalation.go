package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var escalationCmd = &cobra.Command{
	Use:   "escalation",
	Short: "Manage crew escalations",
}

var escalationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List escalations for a crew",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		crewSlug, _ := cmd.Flags().GetString("crew")
		statusFilter, _ := cmd.Flags().GetString("status")

		if crewSlug == "" {
			return fmt.Errorf("--crew is required (crew slug or ID)")
		}

		client := newAPIClient()

		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}
		path := "/api/v1/crews/" + crewID + "/escalations"

		if statusFilter != "" {
			path += "?status=" + statusFilter
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var escalations []struct {
			ID        string  `json:"id"`
			FromName  string  `json:"from_name"`
			FromSlug  string  `json:"from_slug"`
			Reason    string  `json:"reason"`
			Status    string  `json:"status"`
			CreatedAt string  `json:"created_at"`
			Context   *string `json:"context"`
		}
		if err := cli.ReadJSON(resp, &escalations); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "FROM", "REASON", "STATUS", "CREATED"}
		var rows [][]string
		for _, e := range escalations {
			reason := e.Reason
			if len(reason) > 50 {
				reason = reason[:47] + "..."
			}
			idStr := e.ID
			if len(idStr) > 12 {
				idStr = idStr[:12]
			}
			rows = append(rows, []string{idStr, e.FromSlug, reason, e.Status, e.CreatedAt})
		}
		return f.Auto(escalations, headers, rows)
	},
}

var escalationResolveCmd = &cobra.Command{
	Use:   "resolve <id>",
	Short: "Mark an escalation as resolved",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		resolution, _ := cmd.Flags().GetString("resolution")
		body := map[string]interface{}{}
		if resolution != "" {
			body["resolution"] = resolution
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/escalations/"+args[0]+"/resolve", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Escalation %s resolved.", args[0]))
		return nil
	},
}

func init() {
	escalationListCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	escalationListCmd.Flags().String("status", "", "Filter by status: PENDING|RESOLVED")

	escalationResolveCmd.Flags().String("resolution", "", "Resolution notes")

	escalationCmd.AddCommand(escalationListCmd)
	escalationCmd.AddCommand(escalationResolveCmd)
}
