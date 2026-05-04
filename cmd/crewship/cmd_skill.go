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
			ID         string  `json:"id"`
			Slug       string  `json:"slug"`
			Name       string  `json:"display_name"`
			Category   string  `json:"category"`
			Version    string  `json:"version"`
			Source     string  `json:"source"`
			Vendor     *string `json:"vendor"`
			Maturity   string  `json:"maturity"`
			ScanStatus string  `json:"scan_status"`
		}
		if err := cli.ReadJSON(resp, &skills); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "VENDOR", "NAME", "CATEGORY", "MATURITY", "SOURCE", "SCAN"}
		var rows [][]string
		for _, s := range skills {
			vendor := "—"
			if s.Vendor != nil && *s.Vendor != "" {
				vendor = *s.Vendor
			}
			rows = append(rows, []string{s.Slug, vendor, s.Name, s.Category, s.Maturity, s.Source, s.ScanStatus})
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
		author := "-"
		if skill.Author != nil {
			author = *skill.Author
		}
		tools := "-"
		if skill.ToolCount != nil {
			tools = fmt.Sprintf("%d", *skill.ToolCount)
		}
		// Description rendered separately via glamour below (see bottom).
		pairs := [][]string{
			{"Name", skill.Name},
			{"Slug", skill.Slug},
			{"ID", skill.ID},
			{"Category", skill.Category},
			{"Version", skill.Version},
			{"Source", skill.Source},
			{"Author", author},
			{"Tools", tools},
			{"Created", skill.CreatedAt},
		}
		if err := f.AutoDetail(skill, pairs); err != nil {
			return err
		}

		// Render markdown description below the metadata table, but ONLY for
		// human-facing formats. JSON/YAML/quiet already include description
		// in the serialized struct.
		if skill.Description != nil && *skill.Description != "" &&
			(f.Format == "" || f.Format == "table") {
			fmt.Fprintln(f.Writer)
			fmt.Fprintf(f.Writer, "%sDescription:%s\n", cli.Bold, cli.Reset)
			f.Markdown(*skill.Description)
		}
		return nil
	},
}

var skillImportCmd = &cobra.Command{
	Use:   "import [url]",
	Short: "Import skill(s): a single SKILL.md from URL/file, or a whole repo with --repo",
	Long: `Import skill(s) into the workspace.

Single SKILL.md from a URL or local file:
  crewship skill import https://raw.githubusercontent.com/owner/repo/main/skills/my-skill/SKILL.md
  crewship skill import --file ./SKILL.md

Whole git repo (walks for **/SKILL.md, license-gated):
  crewship skill import --repo https://github.com/anthropics/skills
  crewship skill import --repo https://github.com/vercel-labs/agent-skills --paths 'skills/*' --dry-run
  crewship skill import --repo https://github.com/foo/private --unsafe-license

The --repo flow shells out to git on the server with --depth 1 --filter=blob:none. Each SKILL.md is parsed, license-checked against the SPDX allowlist (MIT, Apache-2.0, BSD-2/3, ISC, CC0-1.0, MPL-2.0, Unlicense, 0BSD), and then written. --unsafe-license overrides the allowlist for one batch. --dry-run reports what would import without writing rows.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		fileFlag, _ := cmd.Flags().GetString("file")
		repoFlag, _ := cmd.Flags().GetString("repo")
		refFlag, _ := cmd.Flags().GetString("ref")
		pathsFlag, _ := cmd.Flags().GetStringSlice("paths")
		vendorFlag, _ := cmd.Flags().GetString("vendor")
		unsafeFlag, _ := cmd.Flags().GetBool("unsafe-license")
		dryFlag, _ := cmd.Flags().GetBool("dry-run")

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		if wsID == "" {
			return fmt.Errorf("workspace ID could not be resolved")
		}

		// --repo path takes priority — single-skill flags are ignored
		// when --repo is set so the caller can't accidentally do both.
		if repoFlag != "" {
			body := map[string]interface{}{
				"git_url":              repoFlag,
				"git_ref":              refFlag,
				"paths":                pathsFlag,
				"vendor":               vendorFlag,
				"allow_unsafe_license": unsafeFlag,
				"dry_run":              dryFlag,
			}
			resp, err := client.Post("/api/v1/workspaces/"+wsID+"/skills/bulk-import", body)
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var result struct {
				Source        string `json:"source"`
				TotalFound    int    `json:"total_found"`
				TotalImported int    `json:"total_imported"`
				Imported      []struct {
					SkillID string `json:"skill_id"`
					Slug    string `json:"slug"`
					Created bool   `json:"created"`
				} `json:"imported"`
				Skipped []struct {
					Path   string `json:"path"`
					Slug   string `json:"slug"`
					Reason string `json:"reason"`
				} `json:"skipped"`
			}
			if err := cli.ReadJSON(resp, &result); err != nil {
				return err
			}
			fmt.Printf("Source: %s\n", result.Source)
			fmt.Printf("Found %d SKILL.md files; imported %d\n", result.TotalFound, result.TotalImported)
			for _, s := range result.Imported {
				verb := "updated"
				if s.Created {
					verb = "created"
				}
				fmt.Printf("  + %s %s (%s)\n", verb, s.Slug, s.SkillID)
			}
			if len(result.Skipped) > 0 {
				fmt.Printf("Skipped (%d):\n", len(result.Skipped))
				for _, s := range result.Skipped {
					fmt.Printf("  - %s — %s\n", s.Path, s.Reason)
				}
			}
			cli.PrintSuccess(fmt.Sprintf("Bulk import complete: %d/%d", result.TotalImported, result.TotalFound))
			return nil
		}

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
			return fmt.Errorf("provide a URL argument, --file, or --repo")
		}

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

var skillCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Generate a new skill via LLM authoring (skill-creator pattern)",
	Long: `Generate a new SKILL.md from a free-form prompt.

The server calls Anthropic with a condensed skill-creator system prompt
(github.com/anthropics/skills/skills/skill-creator) and writes the
result to the workspace skills table with source=GENERATED.

Requires an active Anthropic API key credential in the workspace
(provider=ANTHROPIC, type=API_KEY). Add one under Settings ›
Credentials before running.

Example:
  crewship skill create --slug pdf-cleanup \
    --prompt "Help users sanitise PDFs: strip metadata, remove embedded JS, flatten forms"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		slug, _ := cmd.Flags().GetString("slug")
		prompt, _ := cmd.Flags().GetString("prompt")
		model, _ := cmd.Flags().GetString("model")
		printOnly, _ := cmd.Flags().GetBool("print")

		if slug == "" || prompt == "" {
			return fmt.Errorf("--slug and --prompt are required")
		}

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		if wsID == "" {
			return fmt.Errorf("workspace ID could not be resolved")
		}

		body := map[string]interface{}{
			"slug":   slug,
			"prompt": prompt,
		}
		if model != "" {
			body["model"] = model
		}

		resp, err := client.Post("/api/v1/workspaces/"+wsID+"/skills/generate", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			SkillID    string `json:"skill_id"`
			Slug       string `json:"slug"`
			Content    string `json:"content"`
			ScanStatus string `json:"scan_status"`
			ScanReason string `json:"scan_reason"`
			Quality    string `json:"description_quality"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		if printOnly {
			fmt.Println(result.Content)
			return nil
		}

		cli.PrintSuccess(fmt.Sprintf("Generated skill: %s (%s)", result.Slug, result.SkillID))
		if result.Quality != "" {
			fmt.Fprintf(os.Stderr, "Description quality: %s\n", result.Quality)
		}
		if result.ScanStatus == "FLAGGED" {
			fmt.Fprintf(os.Stderr, "Scan status: FLAGGED — %s\n", result.ScanReason)
			fmt.Fprintf(os.Stderr, "Review the skill body before assigning to an agent.\n")
		}
		return nil
	},
}

func init() {
	skillImportCmd.Flags().String("file", "", "Path to local SKILL.md file (single skill)")
	skillImportCmd.Flags().String("repo", "", "Git URL to clone and walk for **/SKILL.md (bulk import)")
	skillImportCmd.Flags().String("ref", "", "Git ref (branch/tag) — only with --repo; defaults to repo's default branch")
	skillImportCmd.Flags().StringSlice("paths", nil, "Glob filters relative to repo root — only with --repo")
	skillImportCmd.Flags().String("vendor", "", "Override vendor namespace for imported skills (defaults to 'community')")
	skillImportCmd.Flags().Bool("unsafe-license", false, "Skip the SPDX license allowlist (use with caution)")
	skillImportCmd.Flags().Bool("dry-run", false, "Walk and parse but don't write to DB")

	skillCreateCmd.Flags().String("slug", "", "Skill slug (kebab-case identifier)")
	skillCreateCmd.Flags().String("prompt", "", "Free-form description of what the skill should do")
	skillCreateCmd.Flags().String("model", "", "Override LLM model (default: claude-sonnet-4-6)")
	skillCreateCmd.Flags().Bool("print", false, "Print generated SKILL.md to stdout instead of summary")

	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillGetCmd)
	skillCmd.AddCommand(skillImportCmd)
	skillCmd.AddCommand(skillCreateCmd)
	skillCmd.AddCommand(skillAssignCmd)
	skillCmd.AddCommand(skillUnassignCmd)
}
