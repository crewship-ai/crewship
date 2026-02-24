package main

import (
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage skills",
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all skills in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/skills")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var skills []struct {
			ID       string `json:"id"`
			Slug     string `json:"slug"`
			Name     string `json:"display_name"`
			Category string `json:"category"`
			Version  string `json:"version"`
			Source   string `json:"source"`
		}
		if err := cli.ReadJSON(resp, &skills); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "NAME", "CATEGORY", "VERSION", "SOURCE"}
		var rows [][]string
		for _, s := range skills {
			rows = append(rows, []string{s.Slug, s.Name, s.Category, s.Version, s.Source})
		}
		return f.Auto(skills, headers, rows)
	},
}

var skillGetCmd = &cobra.Command{
	Use:   "get <slug-or-id>",
	Short: "Show skill details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		skillID, err := resolveSkillID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/skills/" + skillID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var skill struct {
			ID          string  `json:"id"`
			Name        string  `json:"display_name"`
			Slug        string  `json:"slug"`
			Category    string  `json:"category"`
			Version     string  `json:"version"`
			Source      string  `json:"source"`
			Description *string `json:"description"`
			Author      *string `json:"author"`
			ToolCount   *int    `json:"tool_count"`
			CreatedAt   string  `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &skill); err != nil {
			return err
		}

		f := newFormatter()
		desc := "-"
		if skill.Description != nil {
			desc = *skill.Description
		}
		author := "-"
		if skill.Author != nil {
			author = *skill.Author
		}
		tools := "-"
		if skill.ToolCount != nil {
			tools = fmt.Sprintf("%d", *skill.ToolCount)
		}
		pairs := [][]string{
			{"Name", skill.Name},
			{"Slug", skill.Slug},
			{"ID", skill.ID},
			{"Category", skill.Category},
			{"Version", skill.Version},
			{"Source", skill.Source},
			{"Author", author},
			{"Description", desc},
			{"Tools", tools},
			{"Created", skill.CreatedAt},
		}
		return f.AutoDetail(skill, pairs)
	},
}

var skillImportCmd = &cobra.Command{
	Use:   "import <url>",
	Short: "Import a skill from URL or local file",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		fileFlag, _ := cmd.Flags().GetString("file")
		wsID := cli.ResolveWorkspace(flagWorkspace, cliCfg)

		body := map[string]interface{}{}

		if fileFlag != "" {
			data, err := os.ReadFile(fileFlag)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			body["content"] = string(data)
			body["source"] = "file"
		} else if len(args) > 0 {
			body["url"] = args[0]
			body["source"] = "url"
		} else {
			return fmt.Errorf("provide a URL argument or --file flag")
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/workspaces/"+wsID+"/skills/import", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"display_name"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Skill imported: %s (%s)", result.Slug, result.ID))
		return nil
	},
}

var skillAssignCmd = &cobra.Command{
	Use:   "assign <skill-slug> <agent-slug>",
	Short: "Assign a skill to an agent",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		skillID, err := resolveSkillID(client, args[0])
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[1])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/agents/"+agentID+"/skills", map[string]string{
			"skill_id": skillID,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Skill %s assigned to agent %s", args[0], args[1]))
		return nil
	},
}

var skillUnassignCmd = &cobra.Command{
	Use:   "unassign <skill-slug> <agent-slug>",
	Short: "Remove a skill from an agent",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		skillID, err := resolveSkillID(client, args[0])
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[1])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/agents/" + agentID + "/skills/" + skillID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Skill %s removed from agent %s", args[0], args[1]))
		return nil
	},
}

func resolveSkillID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		return slugOrID, nil
	}

	resp, err := client.Get("/api/v1/skills")
	if err != nil {
		return "", fmt.Errorf("resolve skill: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var skills []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &skills); err != nil {
		return "", err
	}

	for _, s := range skills {
		if s.Slug == slugOrID {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("skill not found: %s", slugOrID)
}

func init() {
	skillImportCmd.Flags().String("file", "", "Path to local SKILL.md file")

	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillGetCmd)
	skillCmd.AddCommand(skillImportCmd)
	skillCmd.AddCommand(skillAssignCmd)
	skillCmd.AddCommand(skillUnassignCmd)
}
