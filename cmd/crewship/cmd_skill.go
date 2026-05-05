package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// boolFlag turns a CLI bool into the "1"/"" string the API expects so we can
// pass it through queryString without a special-case caller.
func boolFlag(v bool) string {
	if v {
		return "1"
	}
	return ""
}

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage skills",
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all skills in the workspace",
	Long: `List skills in the workspace, optionally narrowed by metadata or installation state.

Filters compose: --category=CODING --maturity=OFFICIAL returns OFFICIAL skills
in CODING. --installed-for restricts to skills currently assigned to a single
agent (slug or ID); --installed alone returns the workspace-wide installed set.

Examples:
  crewship skill list --maturity OFFICIAL
  crewship skill list --vendor anthropic --category DESIGN
  crewship skill list --installed-for viktor
  crewship skill list --search pdf`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		category, _ := cmd.Flags().GetString("category")
		source, _ := cmd.Flags().GetString("source")
		vendor, _ := cmd.Flags().GetString("vendor")
		maturity, _ := cmd.Flags().GetString("maturity")
		runtime, _ := cmd.Flags().GetString("runtime")
		search, _ := cmd.Flags().GetString("search")
		installedFlag, _ := cmd.Flags().GetBool("installed")
		installedForRaw, _ := cmd.Flags().GetString("installed-for")

		var installedFor string
		if installedForRaw != "" {
			id, err := resolveAgentID(client, installedForRaw)
			if err != nil {
				return fmt.Errorf("resolve --installed-for agent: %w", err)
			}
			installedFor = id
		}

		path := "/api/v1/skills" + queryString(
			"category", strings.ToUpper(category),
			"source", strings.ToUpper(source),
			"vendor", vendor,
			"maturity", strings.ToUpper(maturity),
			"runtime", strings.ToUpper(runtime),
			"search", search,
			"installed_for_agent_id", installedFor,
			"installed", boolFlag(installedFlag),
		)

		resp, err := client.Get(path)
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

		body := map[string]interface{}{
			"allow_unsafe_license": unsafeFlag,
		}
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
	Use:   "assign <skill-slug> [agent-slug]",
	Short: "Assign a skill to an agent or to every agent in a crew",
	Long: `Assign a skill to one agent, several agents, or every agent in a crew.

Single agent (positional, backwards-compatible with v0.1):
  crewship skill assign my-skill viktor

Multiple agents at once:
  crewship skill assign my-skill --to-agents viktor,nela,martin

Whole crew:
  crewship skill assign my-skill --to-crew engineering

The crew form resolves the crew slug, fans out to every agent in it,
and reports a per-agent summary. Already-assigned agents are treated
as success (idempotent — same shape as the API).`,
	Args: cobra.RangeArgs(1, 2),
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

		toAgents, _ := cmd.Flags().GetStringSlice("to-agents")
		toCrew, _ := cmd.Flags().GetString("to-crew")

		targets, err := resolveAssignTargets(client, args, toAgents, toCrew)
		if err != nil {
			return err
		}
		return runAssignFanout(client, skillID, args[0], targets, "assign")
	},
}

var skillUnassignCmd = &cobra.Command{
	Use:   "unassign <skill-slug> [agent-slug]",
	Short: "Remove a skill from an agent, several agents, or a whole crew",
	Long: `Inverse of assign. Same target flags:
  --to-agents agent1,agent2  --to-crew engineering

Reports per-agent failures rather than aborting on the first one so a
single missing assignment doesn't block the rest.`,
	Args: cobra.RangeArgs(1, 2),
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

		toAgents, _ := cmd.Flags().GetStringSlice("to-agents")
		toCrew, _ := cmd.Flags().GetString("to-crew")

		targets, err := resolveAssignTargets(client, args, toAgents, toCrew)
		if err != nil {
			return err
		}
		return runAssignFanout(client, skillID, args[0], targets, "unassign")
	},
}

// resolveAssignTargets reconciles the three ways a user can name
// agents (positional arg, --to-agents list, --to-crew slug) into a
// flat list of agent IDs. Exactly one source must be set; mixing them
// would be ambiguous so we reject up-front rather than silently
// preferring one.
func resolveAssignTargets(client *cli.Client, positional, toAgents []string, toCrew string) ([]assignTarget, error) {
	hasPositional := len(positional) >= 2 && positional[1] != ""
	hasList := len(toAgents) > 0
	hasCrew := toCrew != ""

	count := 0
	for _, b := range []bool{hasPositional, hasList, hasCrew} {
		if b {
			count++
		}
	}
	if count == 0 {
		return nil, fmt.Errorf("specify an agent (positional), --to-agents=a,b or --to-crew=<slug>")
	}
	if count > 1 {
		return nil, fmt.Errorf("pick one of: positional agent, --to-agents, --to-crew")
	}

	switch {
	case hasPositional:
		id, err := resolveAgentID(client, positional[1])
		if err != nil {
			return nil, err
		}
		return []assignTarget{{slug: positional[1], id: id}}, nil
	case hasList:
		out := make([]assignTarget, 0, len(toAgents))
		for _, slug := range toAgents {
			slug = strings.TrimSpace(slug)
			if slug == "" {
				continue
			}
			id, err := resolveAgentID(client, slug)
			if err != nil {
				return nil, fmt.Errorf("resolve %q: %w", slug, err)
			}
			out = append(out, assignTarget{slug: slug, id: id})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("--to-agents was empty after parsing")
		}
		return out, nil
	default:
		return resolveCrewMembers(client, toCrew)
	}
}

type assignTarget struct {
	slug string
	id   string
}

// resolveCrewMembers fetches all agents in a crew (by slug or ID) and
// returns them as assign targets. The /api/v1/agents response only
// carries crew_id (not crew_slug), so we resolve slug → id first via
// /api/v1/crews and then filter the agent list locally. An earlier
// implementation tried to match on a non-existent crew_slug field
// and fell back to "return every agent in the workspace" when the
// match was empty — which is exactly the kind of broadcast a fan-out
// command must NEVER do.
func resolveCrewMembers(client *cli.Client, crewSlugOrID string) ([]assignTarget, error) {
	crewID, err := resolveCrewID(client, crewSlugOrID)
	if err != nil {
		return nil, err
	}

	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var agents []struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		CrewID string `json:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	var out []assignTarget
	for _, a := range agents {
		if a.CrewID == crewID {
			out = append(out, assignTarget{slug: a.Slug, id: a.ID})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("crew %q has no agents", crewSlugOrID)
	}
	return out, nil
}

// runAssignFanout calls the per-agent endpoint for each target and
// surfaces a per-agent table at the end. Errors are collected, not
// fatal — if 3 of 5 succeed the user wants to see which 2 failed
// rather than the command aborting on the first.
func runAssignFanout(client *cli.Client, skillID, skillLabel string, targets []assignTarget, op string) error {
	var failures []string
	for _, t := range targets {
		var resp interface {
			StatusCode() int
		}
		_ = resp
		if op == "assign" {
			r, err := client.Post("/api/v1/agents/"+t.id+"/skills", map[string]string{
				"skill_id": skillID,
			})
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", t.slug, err))
				continue
			}
			if err := cli.CheckError(r); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", t.slug, err))
				continue
			}
			r.Body.Close()
		} else {
			r, err := client.Delete("/api/v1/agents/" + t.id + "/skills/" + skillID)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", t.slug, err))
				continue
			}
			if err := cli.CheckError(r); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", t.slug, err))
				continue
			}
			r.Body.Close()
		}
	}

	verb := "assigned to"
	if op == "unassign" {
		verb = "removed from"
	}
	if len(failures) == 0 {
		cli.PrintSuccess(fmt.Sprintf("Skill %s %s %d agent(s)", skillLabel, verb, len(targets)))
		return nil
	}
	for _, f := range failures {
		fmt.Fprintln(os.Stderr, "  ! "+f)
	}
	return fmt.Errorf("%s failed for %d of %d agents", op, len(failures), len(targets))
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
	skillListCmd.Flags().String("category", "", "Filter by category (CODING, DATA, DEVOPS, ...)")
	skillListCmd.Flags().String("source", "", "Filter by source (BUNDLED, CUSTOM, GENERATED, MARKETPLACE, MANAGED)")
	skillListCmd.Flags().String("vendor", "", "Filter by vendor namespace (e.g. anthropic, community)")
	skillListCmd.Flags().String("maturity", "", "Filter by maturity (OFFICIAL, CURATED, COMMUNITY, EXPERIMENTAL)")
	skillListCmd.Flags().String("runtime", "", "Filter by runtime (INSTRUCTIONS, SCRIPT, MCP, HYBRID)")
	skillListCmd.Flags().String("search", "", "Substring match on name / display_name / description")
	skillListCmd.Flags().Bool("installed", false, "Only show skills installed on at least one agent in the workspace")
	skillListCmd.Flags().String("installed-for", "", "Only show skills installed on this agent (slug or ID)")

	skillImportCmd.Flags().String("file", "", "Path to local SKILL.md file (single skill)")
	skillImportCmd.Flags().String("repo", "", "Git URL to clone and walk for **/SKILL.md (bulk import)")
	skillImportCmd.Flags().String("ref", "", "Git ref (branch/tag) — only with --repo; defaults to repo's default branch")
	skillImportCmd.Flags().StringSlice("paths", nil, "Glob filters relative to repo root — only with --repo")
	skillImportCmd.Flags().String("vendor", "", "Override vendor namespace for imported skills (defaults to 'community')")
	skillImportCmd.Flags().Bool("unsafe-license", false, "Skip the SPDX license allowlist (use with caution)")
	skillImportCmd.Flags().Bool("dry-run", false, "Walk and parse but don't write to DB")

	skillAssignCmd.Flags().StringSlice("to-agents", nil, "Comma-separated agent slugs/IDs to assign (alternative to positional)")
	skillAssignCmd.Flags().String("to-crew", "", "Crew slug/ID — assign to every agent in this crew")
	skillUnassignCmd.Flags().StringSlice("to-agents", nil, "Comma-separated agent slugs/IDs to unassign")
	skillUnassignCmd.Flags().String("to-crew", "", "Crew slug/ID — unassign from every agent in this crew")

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
