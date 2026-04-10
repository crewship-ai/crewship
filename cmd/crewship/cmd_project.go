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

func init() {
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectCreateCmd)
	projectCmd.AddCommand(projectGetCmd)

	projectCreateCmd.Flags().String("name", "", "Project name (required)")
	projectCreateCmd.Flags().String("description", "", "Project description")
	projectCreateCmd.Flags().String("color", "", "Hex color (e.g. #3B82F6)")
	projectCreateCmd.Flags().String("status", "planned", "Status: backlog, planned, in_progress, paused, completed, cancelled")
	projectCreateCmd.Flags().String("priority", "none", "Priority: none, low, medium, high, urgent")
	projectCreateCmd.Flags().String("icon", "", "Lucide icon name")
	projectCreateCmd.Flags().String("target-date", "", "Target date (ISO format)")
}
