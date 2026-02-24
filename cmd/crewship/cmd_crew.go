package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewCmd = &cobra.Command{
	Use:   "crew",
	Short: "Manage crews",
}

var crewListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all crews in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/crews")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var crews []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Slug        string  `json:"slug"`
			Description *string `json:"description"`
			MemoryMB    int     `json:"container_memory_mb"`
			CPUs        float64 `json:"container_cpus"`
			AgentCount  int     `json:"_count_agents"`
		}
		if err := cli.ReadJSON(resp, &crews); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "NAME", "AGENTS", "MEMORY", "CPUS"}
		var rows [][]string
		for _, c := range crews {
			rows = append(rows, []string{
				c.Slug, c.Name,
				fmt.Sprintf("%d", c.AgentCount),
				fmt.Sprintf("%dMB", c.MemoryMB),
				fmt.Sprintf("%.1f", c.CPUs),
			})
		}
		return f.Auto(crews, headers, rows)
	},
}

var crewGetCmd = &cobra.Command{
	Use:   "get <slug-or-id>",
	Short: "Show crew details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + crewID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var crew struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Slug        string  `json:"slug"`
			Description *string `json:"description"`
			Color       *string `json:"color"`
			Icon        *string `json:"icon"`
			MemoryMB    int     `json:"container_memory_mb"`
			CPUs        float64 `json:"container_cpus"`
			CreatedAt   string  `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &crew); err != nil {
			return err
		}

		f := newFormatter()
		desc := "-"
		if crew.Description != nil {
			desc = *crew.Description
		}
		pairs := [][]string{
			{"Name", crew.Name},
			{"Slug", crew.Slug},
			{"ID", crew.ID},
			{"Description", desc},
			{"Memory", fmt.Sprintf("%dMB", crew.MemoryMB)},
			{"CPUs", fmt.Sprintf("%.1f", crew.CPUs)},
			{"Created", crew.CreatedAt},
		}
		return f.AutoDetail(crew, pairs)
	},
}

var crewCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new crew",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}

		body := map[string]interface{}{"name": name}
		if v, _ := flags.GetString("slug"); v != "" {
			body["slug"] = v
		}
		if v, _ := flags.GetString("description"); v != "" {
			body["description"] = v
		}
		if v, _ := flags.GetString("color"); v != "" {
			body["color"] = v
		}
		if v, _ := flags.GetString("icon"); v != "" {
			body["icon"] = v
		}
		if v, _ := flags.GetInt("memory-mb"); v > 0 {
			body["container_memory_mb"] = v
		}
		if v, _ := flags.GetFloat64("cpus"); v > 0 {
			body["container_cpus"] = v
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/crews", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Crew created: %s (%s)", created.Slug, created.ID))
		return nil
	},
}

var crewUpdateCmd = &cobra.Command{
	Use:   "update <slug-or-id>",
	Short: "Update a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()
		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("description") {
			v, _ := flags.GetString("description")
			body["description"] = v
		}
		if flags.Changed("color") {
			v, _ := flags.GetString("color")
			body["color"] = v
		}
		if flags.Changed("icon") {
			v, _ := flags.GetString("icon")
			body["icon"] = v
		}
		if flags.Changed("memory-mb") {
			v, _ := flags.GetInt("memory-mb")
			body["container_memory_mb"] = v
		}
		if flags.Changed("cpus") {
			v, _ := flags.GetFloat64("cpus")
			body["container_cpus"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		resp, err := client.Patch("/api/v1/crews/"+crewID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Crew updated successfully.")
		return nil
	},
}

var crewDeleteCmd = &cobra.Command{
	Use:   "delete <slug-or-id>",
	Short: "Delete a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete crew %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/crews/" + crewID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Crew deleted.")
		return nil
	},
}

var crewStatusCmd = &cobra.Command{
	Use:   "status <slug-or-id>",
	Short: "Show live crew status (agents, assignments, escalations)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		// Fetch crew detail
		crewResp, err := client.Get("/api/v1/crews/" + crewID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(crewResp); err != nil {
			return err
		}
		var crew struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if err := cli.ReadJSON(crewResp, &crew); err != nil {
			return err
		}

		// Fetch agents in crew
		agentsResp, err := client.Get("/api/v1/agents?crew_id=" + crewID)
		if err != nil {
			return err
		}
		var agentsList []struct {
			Slug      string `json:"slug"`
			AgentRole string `json:"agent_role"`
			Status    string `json:"status"`
		}
		if err := cli.CheckError(agentsResp); err != nil {
			cli.PrintWarning("could not fetch agents: " + err.Error())
		} else if err := cli.ReadJSON(agentsResp, &agentsList); err != nil {
			cli.PrintWarning("could not parse agents: " + err.Error())
		}

		// Fetch assignments
		assignResp, err := client.Get("/api/v1/crews/" + crewID + "/assignments")
		if err != nil {
			return err
		}
		var assignmentsList []struct {
			Task           string  `json:"task"`
			Status         string  `json:"status"`
			AssignedBySlug string  `json:"assigned_by_slug"`
			AssignedToSlug *string `json:"assigned_to_slug"`
		}
		if err := cli.CheckError(assignResp); err != nil {
			cli.PrintWarning("could not fetch assignments: " + err.Error())
		} else if err := cli.ReadJSON(assignResp, &assignmentsList); err != nil {
			cli.PrintWarning("could not parse assignments: " + err.Error())
		}

		// Fetch escalations
		escResp, err := client.Get("/api/v1/crews/" + crewID + "/escalations")
		if err != nil {
			return err
		}
		var escalationsList []struct {
			Reason string `json:"reason"`
			Status string `json:"status"`
		}
		if err := cli.CheckError(escResp); err != nil {
			cli.PrintWarning("could not fetch escalations: " + err.Error())
		} else if err := cli.ReadJSON(escResp, &escalationsList); err != nil {
			cli.PrintWarning("could not parse escalations: " + err.Error())
		}

		// Display compound view
		fmt.Printf("%sCrew: %s%s (%s)\n\n", cli.Bold, crew.Name, cli.Reset, crew.Slug)

		fmt.Printf("%sAGENTS (%d):%s\n", cli.Bold, len(agentsList), cli.Reset)
		if len(agentsList) > 0 {
			w := cli.NewFormatter("table")
			headers := []string{"SLUG", "ROLE", "STATUS"}
			var rows [][]string
			for _, a := range agentsList {
				rows = append(rows, []string{a.Slug, a.AgentRole, a.Status})
			}
			w.Table(headers, rows)
		} else {
			fmt.Println("  No agents")
		}

		fmt.Printf("\n%sASSIGNMENTS (last 5):%s\n", cli.Bold, cli.Reset)
		if len(assignmentsList) > 0 {
			limit := 5
			if len(assignmentsList) < limit {
				limit = len(assignmentsList)
			}
			for _, a := range assignmentsList[:limit] {
				to := "-"
				if a.AssignedToSlug != nil {
					to = *a.AssignedToSlug
				}
				task := a.Task
				if utf8.RuneCountInString(task) > 60 {
					task = string([]rune(task)[:57]) + "..."
				}
				fmt.Printf("  %s%-10s%s %s -> %s: %q\n", statusColor(a.Status), a.Status, cli.Reset, a.AssignedBySlug, to, task)
			}
		} else {
			fmt.Println("  No assignments")
		}

		fmt.Printf("\n%sESCALATIONS (open):%s\n", cli.Bold, cli.Reset)
		openEsc := 0
		for _, e := range escalationsList {
			if e.Status == "PENDING" || e.Status == "OPEN" {
				fmt.Printf("  %s%s%s\n", cli.Yellow, e.Reason, cli.Reset)
				openEsc++
			}
		}
		if openEsc == 0 {
			fmt.Println("  None")
		}

		return nil
	},
}

func statusColor(status string) string {
	switch strings.ToUpper(status) {
	case "COMPLETED":
		return cli.Green
	case "RUNNING", "IN_PROGRESS":
		return cli.Blue
	case "PENDING":
		return cli.Yellow
	case "FAILED", "ERROR":
		return cli.Red
	default:
		return ""
	}
}

var crewMemberCmd = &cobra.Command{
	Use:   "member",
	Short: "Manage crew members",
}

var crewMemberListCmd = &cobra.Command{
	Use:   "list <crew-slug-or-id>",
	Short: "List crew members",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + crewID + "/members")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var members []struct {
			ID   string `json:"id"`
			User struct {
				ID       string `json:"id"`
				Email    string `json:"email"`
				FullName string `json:"full_name"`
			} `json:"user"`
			JoinedAt string `json:"joined_at"`
		}
		if err := cli.ReadJSON(resp, &members); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "USER", "EMAIL", "JOINED"}
		var rows [][]string
		for _, m := range members {
			rows = append(rows, []string{m.ID, m.User.FullName, m.User.Email, m.JoinedAt})
		}
		return f.Auto(members, headers, rows)
	},
}

var crewMemberAddCmd = &cobra.Command{
	Use:   "add <crew-slug-or-id> <user-id>",
	Short: "Add a user to a crew",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/members", map[string]string{
			"user_id": args[1],
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Member added to crew.")
		return nil
	},
}

var crewMemberRemoveCmd = &cobra.Command{
	Use:   "remove <crew-slug-or-id> <member-id>",
	Short: "Remove a member from a crew",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/crews/" + crewID + "/members/" + args[1])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Member removed from crew.")
		return nil
	},
}

func init() {
	crewCreateCmd.Flags().String("name", "", "Crew name (required)")
	crewCreateCmd.Flags().String("slug", "", "Crew slug (auto from name)")
	crewCreateCmd.Flags().String("description", "", "Description")
	crewCreateCmd.Flags().String("color", "", "Hex color (#3B82F6)")
	crewCreateCmd.Flags().String("icon", "", "Emoji icon")
	crewCreateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewCreateCmd.Flags().Float64("cpus", 0, "Container CPU limit")

	crewUpdateCmd.Flags().String("name", "", "Crew name")
	crewUpdateCmd.Flags().String("description", "", "Description")
	crewUpdateCmd.Flags().String("color", "", "Hex color")
	crewUpdateCmd.Flags().String("icon", "", "Emoji icon")
	crewUpdateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewUpdateCmd.Flags().Float64("cpus", 0, "Container CPU limit")

	crewDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	crewMemberCmd.AddCommand(crewMemberListCmd)
	crewMemberCmd.AddCommand(crewMemberAddCmd)
	crewMemberCmd.AddCommand(crewMemberRemoveCmd)

	crewCmd.AddCommand(crewListCmd)
	crewCmd.AddCommand(crewGetCmd)
	crewCmd.AddCommand(crewCreateCmd)
	crewCmd.AddCommand(crewUpdateCmd)
	crewCmd.AddCommand(crewDeleteCmd)
	crewCmd.AddCommand(crewStatusCmd)
	crewCmd.AddCommand(crewMemberCmd)
}
