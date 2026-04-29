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
			ID           string  `json:"id"`
			Name         string  `json:"name"`
			Slug         string  `json:"slug"`
			Description  *string `json:"description"`
			MemoryMB     int     `json:"container_memory_mb"`
			CPUs         float64 `json:"container_cpus"`
			NetworkMode  string  `json:"network_mode"`
			RuntimeImage *string `json:"runtime_image"`
			CachedImage  *string `json:"cached_image"`
			Count        struct {
				Agents  int `json:"agents"`
				Members int `json:"members"`
			} `json:"_count"`
		}
		if err := cli.ReadJSON(resp, &crews); err != nil {
			return err
		}

		showRuntime, _ := cmd.Flags().GetBool("runtime")

		f := newFormatter()
		headers := []string{"SLUG", "NAME", "AGENTS", "NETWORK", "MEMORY", "CPUS"}
		if showRuntime {
			headers = append(headers, "RUNTIME", "CACHED", "PROVISIONED")
		}
		var rows [][]string
		for _, c := range crews {
			nm := c.NetworkMode
			if nm == "" {
				nm = "free"
			}
			row := []string{
				c.Slug, c.Name,
				fmt.Sprintf("%d", c.Count.Agents),
				nm,
				fmt.Sprintf("%dMB", c.MemoryMB),
				fmt.Sprintf("%.1f", c.CPUs),
			}
			if showRuntime {
				runtime := "—"
				if c.RuntimeImage != nil && *c.RuntimeImage != "" {
					runtime = truncateImageRef(*c.RuntimeImage)
				}
				cached := "—"
				provisioned := "no"
				if c.CachedImage != nil && *c.CachedImage != "" {
					cached = truncateImageRef(*c.CachedImage)
					provisioned = "yes"
				}
				row = append(row, runtime, cached, provisioned)
			}
			rows = append(rows, row)
		}
		return f.Auto(crews, headers, rows)
	},
}

// truncateImageRef shortens a container image reference for display by stripping
// the registry/path prefix and, for cache images, truncating the long hash tag.
// Examples:
//
//	mcr.microsoft.com/devcontainers/javascript-node:22-bookworm -> javascript-node:22-bookworm
//	crewship-cache:02be226ac713abcd -> cache:02be226a

func truncateImageRef(ref string) string {
	// Strip registry/path prefix — keep only the last path segment.
	name := ref
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		name = ref[idx+1:]
	}
	// For crewship-cache images, shorten long hash tags to 8 chars.
	if strings.HasPrefix(name, "crewship-cache:") {
		tag := strings.TrimPrefix(name, "crewship-cache:")
		if len(tag) > 8 {
			tag = tag[:8]
		}
		return "cache:" + tag
	}
	return name
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

func init() {
	crewListCmd.Flags().Bool("runtime", false, "Include runtime image, cached image, and provisioning status columns")

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
