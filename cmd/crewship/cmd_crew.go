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
			NetworkMode string  `json:"network_mode"`
			AgentCount  int     `json:"_count_agents"`
		}
		if err := cli.ReadJSON(resp, &crews); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "NAME", "AGENTS", "NETWORK", "MEMORY", "CPUS"}
		var rows [][]string
		for _, c := range crews {
			nm := c.NetworkMode
			if nm == "" {
				nm = "free"
			}
			rows = append(rows, []string{
				c.Slug, c.Name,
				fmt.Sprintf("%d", c.AgentCount),
				nm,
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
			ID             string   `json:"id"`
			Name           string   `json:"name"`
			Slug           string   `json:"slug"`
			Description    *string  `json:"description"`
			Color          *string  `json:"color"`
			Icon           *string  `json:"icon"`
			MemoryMB       int      `json:"container_memory_mb"`
			CPUs           float64  `json:"container_cpus"`
			TTLHours       *int     `json:"container_ttl_hours"`
			NetworkMode    string   `json:"network_mode"`
			AllowedDomains []string `json:"allowed_domains"`
			CreatedAt      string   `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &crew); err != nil {
			return err
		}

		f := newFormatter()
		desc := "-"
		if crew.Description != nil {
			desc = *crew.Description
		}
		domainsStr := "-"
		if len(crew.AllowedDomains) > 0 {
			domainsStr = strings.Join(crew.AllowedDomains, ", ")
		}
		networkMode := crew.NetworkMode
		if networkMode == "" {
			networkMode = "free"
		}
		ttlStr := "Never stop"
		if crew.TTLHours != nil && *crew.TTLHours > 0 {
			ttlStr = fmt.Sprintf("%d hours", *crew.TTLHours)
		}
		pairs := [][]string{
			{"Name", crew.Name},
			{"Slug", crew.Slug},
			{"ID", crew.ID},
			{"Description", desc},
			{"Memory", fmt.Sprintf("%dMB", crew.MemoryMB)},
			{"CPUs", fmt.Sprintf("%.1f", crew.CPUs)},
			{"TTL", ttlStr},
			{"Network Mode", networkMode},
			{"Allowed Domains", domainsStr},
			{"Created", crew.CreatedAt},
		}
		return f.AutoDetail(crew, pairs)
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
			return fmt.Errorf("fetch agents: %w", err)
		}
		if err := cli.ReadJSON(agentsResp, &agentsList); err != nil {
			return fmt.Errorf("parse agents: %w", err)
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
			return fmt.Errorf("fetch assignments: %w", err)
		}
		if err := cli.ReadJSON(assignResp, &assignmentsList); err != nil {
			return fmt.Errorf("parse assignments: %w", err)
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
			return fmt.Errorf("fetch escalations: %w", err)
		}
		if err := cli.ReadJSON(escResp, &escalationsList); err != nil {
			return fmt.Errorf("parse escalations: %w", err)
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

var crewConnectCmd = &cobra.Command{
	Use:   "connect <crew-slug-A> <crew-slug-B>",
	Short: "Create a connection between two crews (enables cross-crew task dispatch)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		fromID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		toID, err := resolveCrewID(client, args[1])
		if err != nil {
			return err
		}

		direction, _ := cmd.Flags().GetString("direction")
		body := map[string]interface{}{
			"from_crew_id": fromID,
			"to_crew_id":   toID,
			"direction":    direction,
		}

		resp, err := client.Post("/api/v1/crew-connections", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID string `json:"id"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Crews connected: %s <-> %s (id: %s)", args[0], args[1], created.ID))
		return nil
	},
}

var crewDisconnectCmd = &cobra.Command{
	Use:   "disconnect <connection-id>",
	Short: "Remove a crew connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/crew-connections/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Crew connection removed.")
		return nil
	},
}

var crewConnectionsCmd = &cobra.Command{
	Use:   "connections",
	Short: "List all crew connections in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/crew-connections")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var conns []struct {
			ID           string `json:"id"`
			FromCrewSlug string `json:"from_crew_slug"`
			ToCrewSlug   string `json:"to_crew_slug"`
			Direction    string `json:"direction"`
			Status       string `json:"status"`
			CreatedAt    string `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &conns); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "FROM", "TO", "DIRECTION", "STATUS", "CREATED"}
		var rows [][]string
		for _, c := range conns {
			rows = append(rows, []string{c.ID, c.FromCrewSlug, c.ToCrewSlug, c.Direction, c.Status, c.CreatedAt})
		}
		return f.Auto(conns, headers, rows)
	},
}

var crewStandupCmd = &cobra.Command{
	Use:   "standup <slug-or-id>",
	Short: "Show crew standup summary (last 24h activity)",
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

		path := "/api/v1/crews/" + crewID + "/standup"
		if since, _ := cmd.Flags().GetString("since"); since != "" {
			path += "?since=" + since
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result map[string]interface{}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(result)
		}
		// Standup returns a text summary
		if text, ok := result["standup"].(string); ok {
			fmt.Println(text)
		} else {
			return f.JSON(result)
		}
		return nil
	},
}

var crewPeerConvsCmd = &cobra.Command{
	Use:   "peer-conversations <slug-or-id>",
	Short: "List peer conversations in a crew",
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

		limit, _ := cmd.Flags().GetInt("limit")
		path := fmt.Sprintf("/api/v1/crews/%s/peer-conversations?limit=%d", crewID, limit)

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		type peerConv struct {
			ID        string  `json:"id"`
			FromName  string  `json:"from_name"`
			ToName    string  `json:"to_name"`
			Question  string  `json:"question"`
			Response  *string `json:"response"`
			Status    string  `json:"status"`
			Escalated bool    `json:"escalated"`
			CreatedAt string  `json:"created_at"`
		}
		var items []peerConv
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "FROM", "TO", "QUESTION", "STATUS", "ESCALATED", "CREATED"}
		rows := make([][]string, len(items))
		for i, item := range items {
			q := item.Question
			if len(q) > 60 {
				q = q[:57] + "..."
			}
			esc := ""
			if item.Escalated {
				esc = "YES"
			}
			rows[i] = []string{item.ID[:8], item.FromName, item.ToName, q, item.Status, esc, item.CreatedAt}
		}
		return f.Auto(items, headers, rows)
	},
}

func init() {
	crewConnectCmd.Flags().String("direction", "bidirectional", "Connection direction: bidirectional or unidirectional")

	crewStandupCmd.Flags().String("since", "", "Show activity since (RFC3339, default: 24h ago)")

	crewPeerConvsCmd.Flags().Int("limit", 50, "Number of conversations to show")

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
	crewCmd.AddCommand(crewConnectCmd)
	crewCmd.AddCommand(crewDisconnectCmd)
	crewCmd.AddCommand(crewConnectionsCmd)
	crewCmd.AddCommand(crewStandupCmd)
	crewCmd.AddCommand(crewPeerConvsCmd)
	crewCmd.AddCommand(crewSuggestCmd)
}
