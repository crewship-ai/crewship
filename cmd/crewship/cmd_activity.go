package main

import (
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var activityCmd = &cobra.Command{
	Use:   "activity",
	Short: "View activity feed across all crews",
	Long: `View the cross-crew activity feed including assignments, peer conversations, and escalations.

Examples:
  crewship activity
  crewship activity --crew backend-team
  crewship activity --lines 20`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		lines, _ := cmd.Flags().GetInt("lines")
		crewFilter, _ := cmd.Flags().GetString("crew")

		path := fmt.Sprintf("/api/v1/activity?limit=%d", lines)
		if crewFilter != "" {
			crewID, err := resolveCrewID(client, crewFilter)
			if err != nil {
				return err
			}
			path += "&crew_id=" + crewID
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var activities []struct {
			Type      string  `json:"type"`
			CrewSlug  string  `json:"crew_slug"`
			Summary   string  `json:"summary"`
			CreatedAt string  `json:"created_at"`
			FromSlug  *string `json:"from_slug"`
			ToSlug    *string `json:"to_slug"`
		}
		if err := cli.ReadJSON(resp, &activities); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" || f.Format == "yaml" {
			if f.Format == "json" {
				return f.JSON(activities)
			}
			return f.YAML(activities)
		}

		for _, a := range activities {
			ts := a.CreatedAt
			if t, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}

			typeColor := ""
			switch a.Type {
			case "ASSIGNMENT", "assignment":
				typeColor = cli.Blue
			case "COMPLETED", "completed":
				typeColor = cli.Green
			case "ESCALATION", "escalation":
				typeColor = cli.Red
			case "QUERY", "query", "RESPONSE", "response":
				typeColor = cli.Cyan
			default:
				typeColor = cli.Gray
			}

			fmt.Printf("%s%s%s  %s[%-12s]%s  %s%-10s%s  %s\n",
				cli.Dim, ts, cli.Reset,
				typeColor, a.Type, cli.Reset,
				cli.Bold, a.CrewSlug, cli.Reset,
				a.Summary)
		}

		return nil
	},
}

func init() {
	activityCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	activityCmd.Flags().Int("lines", 50, "Number of activity entries")
}
