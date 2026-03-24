package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage crew templates (blueprints for pre-configured crews)",
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available crew templates",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/crew-templates")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var templates []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Slug        string  `json:"slug"`
			Description *string `json:"description"`
			Category    string  `json:"category"`
			IsBuiltin   bool    `json:"is_builtin"`
			Agents      []struct {
				Name string `json:"name"`
			} `json:"agents"`
		}
		if err := cli.ReadJSON(resp, &templates); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "NAME", "CATEGORY", "AGENTS", "BUILTIN", "DESCRIPTION"}
		var rows [][]string
		for _, t := range templates {
			desc := "-"
			if t.Description != nil && *t.Description != "" {
				desc = *t.Description
				if len(desc) > 40 {
					desc = desc[:37] + "..."
				}
			}
			builtin := "no"
			if t.IsBuiltin {
				builtin = "yes"
			}
			rows = append(rows, []string{
				t.Slug,
				t.Name,
				t.Category,
				fmt.Sprintf("%d", len(t.Agents)),
				builtin,
				desc,
			})
		}
		return f.Auto(templates, headers, rows)
	},
}

var templateGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show crew template details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/crew-templates/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var t struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Slug        string  `json:"slug"`
			Description *string `json:"description"`
			Category    string  `json:"category"`
			IsBuiltin   bool    `json:"is_builtin"`
			CreatedAt   string  `json:"created_at"`
			Agents      []struct {
				Name      string  `json:"name"`
				Slug      string  `json:"slug"`
				RoleTitle *string `json:"role_title"`
				AgentRole string  `json:"agent_role"`
			} `json:"agents"`
		}
		if err := cli.ReadJSON(resp, &t); err != nil {
			return err
		}

		f := newFormatter()
		desc := "-"
		if t.Description != nil {
			desc = *t.Description
		}
		builtin := "no"
		if t.IsBuiltin {
			builtin = "yes"
		}

		pairs := [][]string{
			{"Name", t.Name},
			{"Slug", t.Slug},
			{"Category", t.Category},
			{"Builtin", builtin},
			{"Description", desc},
			{"ID", t.ID},
			{"Created", t.CreatedAt},
		}
		f.AutoDetail(t, pairs)

		if len(t.Agents) > 0 && f.Format == "table" {
			fmt.Printf("\n%sAGENTS (%d):%s\n", cli.Bold, len(t.Agents), cli.Reset)
			headers := []string{"SLUG", "NAME", "ROLE", "TYPE"}
			var rows [][]string
			for _, a := range t.Agents {
				roleTitle := "-"
				if a.RoleTitle != nil {
					roleTitle = *a.RoleTitle
				}
				rows = append(rows, []string{a.Slug, a.Name, roleTitle, a.AgentRole})
			}
			w := cli.NewFormatter("table")
			w.Table(headers, rows)
		}

		return nil
	},
}

var templateDeployCmd = &cobra.Command{
	Use:   "deploy <slug>",
	Short: "Deploy a crew template (creates crew + agents)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		slug, _ := cmd.Flags().GetString("slug")

		if name == "" {
			return fmt.Errorf("--name is required (crew name for the deployed template)")
		}

		body := map[string]interface{}{}
		if name != "" {
			body["crew_name"] = name
		}
		if slug != "" {
			body["crew_slug"] = slug
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/crew-templates/"+args[0]+"/deploy", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			CrewID     string   `json:"crew_id"`
			CrewName   string   `json:"crew_name"`
			CrewSlug   string   `json:"crew_slug"`
			AgentCount int      `json:"agent_count"`
			AgentIDs   []string `json:"agent_ids"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf(
			"Template deployed: crew %q (%s) with %d agent(s)",
			result.CrewSlug, result.CrewID, result.AgentCount,
		))
		return nil
	},
}

func init() {
	templateDeployCmd.Flags().String("name", "", "Custom crew name (default: template name)")
	templateDeployCmd.Flags().String("slug", "", "Custom crew slug (auto-generated if omitted)")

	templateCmd.AddCommand(templateListCmd)
	templateCmd.AddCommand(templateGetCmd)
	templateCmd.AddCommand(templateDeployCmd)
}
