package main

import (
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "View audit logs",
	Long: `View audit logs for the workspace.

Examples:
  crewship audit
  crewship audit --action create
  crewship audit --lines 100`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		lines, _ := cmd.Flags().GetInt("lines")
		action, _ := cmd.Flags().GetString("action")

		path := fmt.Sprintf("/api/v1/audit?limit=%d", lines)
		if action != "" {
			path += "&action=" + action
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Data []struct {
				ID         string  `json:"id"`
				Action     string  `json:"action"`
				EntityType string  `json:"entity_type"`
				EntityID   *string `json:"entity_id"`
				UserEmail  *string `json:"user_email"`
				CreatedAt  string  `json:"created_at"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"TIME", "ACTION", "ENTITY", "ENTITY_ID", "USER"}
		var rows [][]string
		for _, a := range result.Data {
			ts := a.CreatedAt
			if t, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			entityID := "-"
			if a.EntityID != nil {
				entityID = *a.EntityID
				if len(entityID) > 12 {
					entityID = entityID[:12]
				}
			}
			user := "-"
			if a.UserEmail != nil {
				user = *a.UserEmail
			}
			rows = append(rows, []string{ts, a.Action, a.EntityType, entityID, user})
		}
		return f.Auto(result.Data, headers, rows)
	},
}

func init() {
	auditCmd.Flags().String("action", "", "Filter by action (create, update, delete)")
	auditCmd.Flags().Int("lines", 50, "Number of audit entries")
}
