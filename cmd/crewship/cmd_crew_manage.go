package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

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
		if v, _ := flags.GetInt("ttl"); v > 0 {
			body["container_ttl_hours"] = v
		}
		if v, _ := flags.GetString("network-mode"); v != "" {
			body["network_mode"] = v
		}
		if v, _ := flags.GetString("allowed-domains"); v != "" {
			domains := strings.Split(v, ",")
			trimmed := make([]string, 0, len(domains))
			for _, d := range domains {
				d = strings.TrimSpace(d)
				if d != "" {
					trimmed = append(trimmed, d)
				}
			}
			body["allowed_domains"] = trimmed
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
		if flags.Changed("ttl") {
			v, _ := flags.GetInt("ttl")
			body["container_ttl_hours"] = v // 0 = clear TTL on server side
		}
		if flags.Changed("network-mode") {
			v, _ := flags.GetString("network-mode")
			body["network_mode"] = v
		}
		if flags.Changed("allowed-domains") {
			v, _ := flags.GetString("allowed-domains")
			if v == "" {
				body["allowed_domains"] = []string{}
			} else {
				domains := strings.Split(v, ",")
				trimmed := make([]string, 0, len(domains))
				for _, d := range domains {
					d = strings.TrimSpace(d)
					if d != "" {
						trimmed = append(trimmed, d)
					}
				}
				body["allowed_domains"] = trimmed
			}
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

var crewSuggestCmd = &cobra.Command{
	Use:   "suggest",
	Short: "Get AI-powered crew suggestions based on a goal",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		goal, _ := cmd.Flags().GetString("goal")
		if goal == "" {
			return fmt.Errorf("--goal is required")
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/crew-ai-suggest", map[string]string{"goal": goal})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			CrewName    string `json:"crew_name"`
			Description string `json:"description"`
			Agents      []struct {
				Name      string `json:"name"`
				RoleTitle string `json:"role_title"`
				AgentRole string `json:"agent_role"`
			} `json:"agents"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		fmt.Printf("Suggested crew: %s\n", result.CrewName)
		fmt.Printf("Description: %s\n\n", result.Description)
		fmt.Println("Agents:")
		for _, a := range result.Agents {
			fmt.Printf("  - %s (%s, %s)\n", a.Name, a.RoleTitle, a.AgentRole)
		}
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
	crewCreateCmd.Flags().Int("ttl", 0, "Auto-stop after idle hours (0 = never stop)")
	crewCreateCmd.Flags().String("network-mode", "", "Network policy mode: free or restricted")
	crewCreateCmd.Flags().String("allowed-domains", "", "Comma-separated allowed domains for restricted mode")

	crewUpdateCmd.Flags().String("name", "", "Crew name")
	crewUpdateCmd.Flags().String("description", "", "Description")
	crewUpdateCmd.Flags().String("color", "", "Hex color")
	crewUpdateCmd.Flags().String("icon", "", "Emoji icon")
	crewUpdateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewUpdateCmd.Flags().Float64("cpus", 0, "Container CPU limit")
	crewUpdateCmd.Flags().Int("ttl", -1, "Auto-stop after idle hours (0 = disable TTL)")
	crewUpdateCmd.Flags().String("network-mode", "", "Network policy mode: free or restricted")
	crewUpdateCmd.Flags().String("allowed-domains", "", "Comma-separated allowed domains for restricted mode")

	crewDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	crewSuggestCmd.Flags().String("goal", "", "What should this crew accomplish? (required)")
}
