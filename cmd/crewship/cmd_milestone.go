package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// projectMilestoneCmd groups milestone management subcommands under `project milestone`.
var projectMilestoneCmd = &cobra.Command{
	Use:     "milestone",
	Aliases: []string{"milestones"},
	Short:   "Manage project milestones",
}

type milestoneItem struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	TargetDate  *string `json:"target_date"`
	Status      string  `json:"status"`
	Position    int     `json:"position"`
	IssueCount  int     `json:"issue_count"`
	DoneCount   int     `json:"done_count"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

var projectMilestoneListCmd = &cobra.Command{
	Use:   "list <project-id-or-slug>",
	Short: "List milestones for a project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/projects/" + args[0] + "/milestones")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var items []milestoneItem
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "NAME", "STATUS", "TARGET", "ISSUES", "POS"}
		var rows [][]string
		for _, m := range items {
			target := "-"
			if m.TargetDate != nil {
				target = *m.TargetDate
			}
			rows = append(rows, []string{
				truncateID(m.ID, 12),
				m.Name,
				m.Status,
				target,
				fmt.Sprintf("%d/%d", m.DoneCount, m.IssueCount),
				fmt.Sprintf("%d", m.Position),
			})
		}
		return f.Auto(items, headers, rows)
	},
}

var projectMilestoneCreateCmd = &cobra.Command{
	Use:   "create <project-id-or-slug>",
	Short: "Create a milestone for a project",
	Args:  cobra.ExactArgs(1),
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
		if v, _ := cmd.Flags().GetString("target-date"); v != "" {
			body["target_date"] = v
		}
		if v, _ := cmd.Flags().GetString("status"); v != "" {
			body["status"] = v
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/projects/"+args[0]+"/milestones", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created milestoneItem
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Milestone created: %s (%s)", created.Name, created.ID))
		return nil
	},
}

var projectMilestoneUpdateCmd = &cobra.Command{
	Use:   "update <milestone-id>",
	Short: "Update a milestone",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		body := map[string]interface{}{}
		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("description") {
			v, _ := flags.GetString("description")
			body["description"] = v
		}
		if flags.Changed("target-date") {
			v, _ := flags.GetString("target-date")
			body["target_date"] = v
		}
		if flags.Changed("status") {
			v, _ := flags.GetString("status")
			body["status"] = v
		}
		if flags.Changed("position") {
			v, _ := flags.GetInt("position")
			body["position"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/milestones/"+args[0], body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Milestone updated.")
		return nil
	},
}

var projectMilestoneDeleteCmd = &cobra.Command{
	Use:     "delete <milestone-id>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a milestone",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete milestone %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/milestones/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Milestone deleted.")
		return nil
	},
}

func init() {
	projectMilestoneCreateCmd.Flags().String("name", "", "Milestone name (required)")
	projectMilestoneCreateCmd.Flags().String("description", "", "Description")
	projectMilestoneCreateCmd.Flags().String("target-date", "", "Target date (ISO 8601)")
	projectMilestoneCreateCmd.Flags().String("status", "", "Status (default: active)")

	projectMilestoneUpdateCmd.Flags().String("name", "", "New name")
	projectMilestoneUpdateCmd.Flags().String("description", "", "New description")
	projectMilestoneUpdateCmd.Flags().String("target-date", "", "New target date (ISO 8601)")
	projectMilestoneUpdateCmd.Flags().String("status", "", "New status")
	projectMilestoneUpdateCmd.Flags().Int("position", 0, "New position")

	projectMilestoneDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	projectMilestoneCmd.AddCommand(projectMilestoneListCmd)
	projectMilestoneCmd.AddCommand(projectMilestoneCreateCmd)
	projectMilestoneCmd.AddCommand(projectMilestoneUpdateCmd)
	projectMilestoneCmd.AddCommand(projectMilestoneDeleteCmd)

	projectCmd.AddCommand(projectMilestoneCmd)
}
