package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var projectCmd = &cobra.Command{
	Use:     "project",
	Aliases: []string{"projects"},
	Short:   "Manage projects",
}

type projectItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Color       string  `json:"color"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	Health      string  `json:"health"`
	LeadName    *string `json:"lead_name"`
	TargetDate  *string `json:"target_date"`
	IssueCount  int     `json:"issue_count"`
	DoneCount   int     `json:"done_count"`
	Progress    int     `json:"progress"`
	CreatedAt   string  `json:"created_at"`
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all projects",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/projects")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var projects []projectItem
		if err := cli.ReadJSON(resp, &projects); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"NAME", "STATUS", "PRIORITY", "HEALTH", "ISSUES", "PROGRESS", "TARGET"}
		var rows [][]string
		for _, p := range projects {
			target := "-"
			if p.TargetDate != nil {
				target = *p.TargetDate
			}
			rows = append(rows, []string{
				p.Name,
				p.Status,
				p.Priority,
				p.Health,
				fmt.Sprintf("%d/%d", p.DoneCount, p.IssueCount),
				fmt.Sprintf("%d%%", p.Progress),
				target,
			})
		}
		return f.Auto(projects, headers, rows)
	},
}

var projectCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}

		body := map[string]interface{}{"name": name}
		if v, _ := cmd.Flags().GetString("description"); v != "" {
			body["description"] = v
		}
		if v, _ := cmd.Flags().GetString("color"); v != "" {
			body["color"] = v
		}
		if v, _ := cmd.Flags().GetString("status"); v != "" {
			body["status"] = v
		}
		if v, _ := cmd.Flags().GetString("priority"); v != "" {
			body["priority"] = v
		}
		if v, _ := cmd.Flags().GetString("icon"); v != "" {
			body["icon"] = v
		}
		if v, _ := cmd.Flags().GetString("target-date"); v != "" {
			body["target_date"] = v
		}
		// Lead + start-date parity with the API surface; the handler
		// already accepted these (project_handler.go:Create), only the
		// CLI was lagging.
		if v, _ := cmd.Flags().GetString("lead-id"); v != "" {
			body["lead_id"] = v
		}
		if v, _ := cmd.Flags().GetString("lead-type"); v != "" {
			body["lead_type"] = v
		}
		if v, _ := cmd.Flags().GetString("start-date"); v != "" {
			body["start_date"] = v
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/projects", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Created project: %s (%s)", created.Name, created.Slug))
		return nil
	},
}

var projectGetCmd = &cobra.Command{
	Use:   "get <id-or-slug>",
	Short: "Show project details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/projects/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var p projectItem
		if err := cli.ReadJSON(resp, &p); err != nil {
			return err
		}

		f := newFormatter()
		lead := "-"
		if p.LeadName != nil {
			lead = *p.LeadName
		}
		target := "-"
		if p.TargetDate != nil {
			target = *p.TargetDate
		}
		desc := "-"
		if p.Description != nil {
			desc = *p.Description
		}
		pairs := [][]string{
			{"Name", p.Name},
			{"ID", p.ID},
			{"Status", p.Status},
			{"Priority", p.Priority},
			{"Health", p.Health},
			{"Lead", lead},
			{"Issues", fmt.Sprintf("%d total, %d done", p.IssueCount, p.DoneCount)},
			{"Progress", fmt.Sprintf("%d%%", p.Progress)},
			{"Target", target},
			{"Description", desc},
		}
		return f.AutoDetail(p, pairs)
	},
}

// projectUpdateCmd patches mutable project fields. All flags optional;
// only Changed() flags are sent. Mirrors the PATCH /projects/{id}
// handler's accept-list (name/description/icon/color/status/priority/
// health/lead_type/lead_id/start_date/target_date).
var projectUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update mutable project fields",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()
		// Strings: only forward when the user actually set the flag, so
		// "I didn't pass --name" doesn't clobber the stored name with an
		// empty string. For nullable columns the user can explicitly set
		// to "" to clear (server accepts string-pointer-empty).
		stringFlags := []struct {
			flag, field string
		}{
			{"name", "name"},
			{"description", "description"},
			{"icon", "icon"},
			{"color", "color"},
			{"status", "status"},
			{"priority", "priority"},
			{"health", "health"},
			{"lead-id", "lead_id"},
			{"lead-type", "lead_type"},
			{"start-date", "start_date"},
			{"target-date", "target_date"},
		}
		for _, sf := range stringFlags {
			if flags.Changed(sf.flag) {
				v, _ := flags.GetString(sf.flag)
				body[sf.field] = v
			}
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update — pass at least one of --name/--status/--priority/--health/--lead-id/--lead-type/--start-date/--target-date/--icon/--color/--description")
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/projects/"+args[0], body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Project %s updated.", args[0]))
		return nil
	},
}

// projectDeleteCmd hits DELETE /projects/{id}. The server unlinks
// missions (sets project_id = NULL) in the same transaction so the
// destroy doesn't cascade into issue loss. We prompt unless --yes.
var projectDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a project (issues are unlinked, not deleted)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete project %q? (issues will be unlinked, not deleted)", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/projects/" + args[0])
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Project %s deleted.", args[0]))
		return nil
	},
}

// projectStatsCmd hits GET /projects/{id}/stats. The response is the
// detail-panel breakdown (totals, by_status, by_assignee, by_label,
// crews). We render the summary inline; --json surfaces the raw shape
// for piping into jq.
var projectStatsCmd = &cobra.Command{
	Use:   "stats <id>",
	Short: "Show project breakdown (status / assignee / label / crews)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/projects/" + args[0] + "/stats")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var stats struct {
			TotalIssues     int            `json:"total_issues"`
			CompletedIssues int            `json:"completed_issues"`
			ByStatus        map[string]int `json:"by_status"`
			ByAssignee      []struct {
				AgentName string `json:"agent_name"`
				Total     int    `json:"total"`
				Completed int    `json:"completed"`
			} `json:"by_assignee"`
			ByLabel []struct {
				LabelName string `json:"label_name"`
				Color     string `json:"color"`
				Count     int    `json:"count"`
			} `json:"by_label"`
			Crews []string `json:"crews"`
		}
		if err := cli.ReadJSON(resp, &stats); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(stats)
		}
		if f.Format == "yaml" {
			return f.YAML(stats)
		}

		pct := 0
		if stats.TotalIssues > 0 {
			pct = stats.CompletedIssues * 100 / stats.TotalIssues
		}
		fmt.Printf("Issues: %d total / %d completed (%d%%)\n", stats.TotalIssues, stats.CompletedIssues, pct)
		if len(stats.ByStatus) > 0 {
			fmt.Println("\nBy status:")
			for s, c := range stats.ByStatus {
				fmt.Printf("  %-15s %d\n", s, c)
			}
		}
		if len(stats.ByAssignee) > 0 {
			fmt.Println("\nBy assignee:")
			for _, a := range stats.ByAssignee {
				fmt.Printf("  %-30s %d total / %d done\n", a.AgentName, a.Total, a.Completed)
			}
		}
		if len(stats.ByLabel) > 0 {
			fmt.Println("\nBy label:")
			for _, l := range stats.ByLabel {
				fmt.Printf("  %-30s %d\n", l.LabelName, l.Count)
			}
		}
		if len(stats.Crews) > 0 {
			fmt.Printf("\nCrews: %v\n", stats.Crews)
		}
		return nil
	},
}

func init() {
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectCreateCmd)
	projectCmd.AddCommand(projectGetCmd)
	projectCmd.AddCommand(projectUpdateCmd)
	projectCmd.AddCommand(projectDeleteCmd)
	projectCmd.AddCommand(projectStatsCmd)

	projectCreateCmd.Flags().String("name", "", "Project name (required)")
	projectCreateCmd.Flags().String("description", "", "Project description")
	projectCreateCmd.Flags().String("color", "", "Hex color (e.g. #3B82F6)")
	projectCreateCmd.Flags().String("status", "planned", "Status: backlog, planned, in_progress, paused, completed, cancelled")
	projectCreateCmd.Flags().String("priority", "none", "Priority: none, low, medium, high, urgent")
	projectCreateCmd.Flags().String("icon", "", "Lucide icon name")
	projectCreateCmd.Flags().String("target-date", "", "Target date (ISO format)")
	projectCreateCmd.Flags().String("lead-id", "", "Lead user or agent ID")
	projectCreateCmd.Flags().String("lead-type", "", "Lead type: user or agent")
	projectCreateCmd.Flags().String("start-date", "", "Start date (ISO format)")

	projectUpdateCmd.Flags().String("name", "", "New name")
	projectUpdateCmd.Flags().String("description", "", "New description")
	projectUpdateCmd.Flags().String("icon", "", "Lucide icon name")
	projectUpdateCmd.Flags().String("color", "", "Hex color")
	projectUpdateCmd.Flags().String("status", "", "Status: backlog, planned, in_progress, paused, completed, cancelled")
	projectUpdateCmd.Flags().String("priority", "", "Priority: none, low, medium, high, urgent")
	projectUpdateCmd.Flags().String("health", "", "Health: on_track, at_risk, off_track, on_hold, complete")
	projectUpdateCmd.Flags().String("lead-id", "", "Lead user or agent ID")
	projectUpdateCmd.Flags().String("lead-type", "", "Lead type: user or agent")
	projectUpdateCmd.Flags().String("start-date", "", "Start date (ISO format)")
	projectUpdateCmd.Flags().String("target-date", "", "Target date (ISO format)")

	projectDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
}
