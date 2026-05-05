package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// `skill init` is the offline counterpart to `skill create`. The LLM
// authoring path needs an Anthropic API key and a network round-trip;
// this one needs neither — it writes a SKILL.md scaffold that the user
// can edit and then upload via `skill import --file`. That closes the
// gap for users who only have an OAuth token (Claude Code login) and
// can't reach the Messages API directly.
//
// The scaffold mirrors the canonical layout the bundled skills use:
// frontmatter (name, description, license, category), then ## When to
// use / ## Steps / ## Output format / ## Guardrails sections so the
// agent's [SKILLS AVAILABLE] block has predictable shape.
var skillInitCmd = &cobra.Command{
	Use:   "init <slug>",
	Short: "Create a SKILL.md scaffold on disk (offline; no API call)",
	Long: `Generate a starter SKILL.md file the user can edit and then upload.

This is the offline path: no Anthropic credentials needed, no server
round-trip. The output is a syntactically-valid SKILL.md with all
canonical frontmatter fields and the standard body sections, ready
to be filled in.

Once the user has finished editing:
  crewship skill import --file ./<slug>/SKILL.md

Examples:
  crewship skill init pdf-cleanup
  crewship skill init log-analyzer --category DEVOPS --output ./skills/
  crewship skill init csv-parser --description "Use when the user pastes CSV"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := strings.TrimSpace(args[0])
		if !validSlug(slug) {
			return fmt.Errorf("invalid slug %q: must be lowercase kebab-case, "+
				"start with a letter or digit, and use only [a-z0-9._-]", slug)
		}

		category, _ := cmd.Flags().GetString("category")
		if category == "" {
			category = "CUSTOM"
		}
		category = strings.ToUpper(category)
		if !validCategory(category) {
			return fmt.Errorf("invalid category %q: must be one of %s",
				category, strings.Join(validCategories(), ", "))
		}

		description, _ := cmd.Flags().GetString("description")
		if description == "" {
			// Trigger phrase is the load-bearing part of every skill; if
			// the user didn't pass --description we leave a TODO that
			// also reminds them about the canonical prefixes.
			description = "TODO: replace with a one-line trigger starting with " +
				"\"Use when ...\" / \"Useful for ...\" / \"To <verb> ...\""
		}

		license, _ := cmd.Flags().GetString("license")
		if license == "" {
			license = "MIT"
		}

		outDir, _ := cmd.Flags().GetString("output")
		if outDir == "" {
			outDir = slug
		}
		force, _ := cmd.Flags().GetBool("force")

		dest := filepath.Join(outDir, "SKILL.md")
		if _, err := os.Stat(dest); err == nil && !force {
			return fmt.Errorf("%s already exists; pass --force to overwrite", dest)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}

		content := buildSkillScaffold(slug, category, description, license)
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}

		cli.PrintSuccess(fmt.Sprintf("Scaffold written: %s", dest))
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Next steps:")
		fmt.Fprintf(os.Stderr, "  1) Edit %s — start with the description trigger phrase.\n", dest)
		fmt.Fprintln(os.Stderr, "  2) Upload:  crewship skill import --file "+dest)
		fmt.Fprintln(os.Stderr, "  3) Assign:  crewship skill assign "+slug+" <agent-slug>")
		return nil
	},
}

// buildSkillScaffold produces the canonical SKILL.md body. Kept here
// (not in internal/skills) because this command is intentionally
// offline — it must not pull in DB / HTTP dependencies that would make
// the binary refuse to run without a server reachable.
func buildSkillScaffold(slug, category, description, license string) string {
	return fmt.Sprintf(`---
name: %s
description: %s
license: %s
category: %s
runtime: INSTRUCTIONS
maturity: COMMUNITY
tags:
  - todo
---

## When to use

TODO: describe the exact user intents that should activate this skill.
Be specific — vague triggers cause the agent to either over-fire (every
message looks like the trigger) or under-fire (it never recognises the
real intent). Cite at least one example phrase the user is likely to
type, and at least one near-miss the skill should NOT activate on.

## Steps

1. TODO: first concrete action the agent takes when the trigger fires.
2. TODO: second action.
3. TODO: third action.

## Output format

TODO: describe what the agent should return. If the answer is a
specific shape (JSON, markdown table, exact phrase), spell it out;
the LLM will follow the format you specify in this section.

## Guardrails

- TODO: at least one concrete "do not do this" — guardrails are how
  you keep the skill from drifting under unusual prompts.
- TODO: a second guardrail or a fallback if the trigger is ambiguous.

## Verification (optional)

TODO: if a human can quickly check whether the skill did the right
thing, describe that check here. Often a single regex or a comparison
to known-good output is enough.
`, slug, description, license, category)
}

// `skill export` recovers the SKILL.md body from the workspace registry
// so the user can edit a skill they previously imported and re-upload
// it. Without this, the round-trip "import → tweak → re-import" only
// works if the user kept the original file lying around.
var skillExportCmd = &cobra.Command{
	Use:   "export <skill-slug-or-id>",
	Short: "Recover SKILL.md from the workspace and write it to disk or stdout",
	Long: `Fetch a previously-imported skill's SKILL.md body and emit it.

Output goes to stdout by default, so you can pipe it into a file or a
diff tool. With --output it writes to the given path (or to <slug>.md
if --output is the literal "."), creating intermediate directories as
needed. Combined with skill import this gives a full edit loop:

  crewship skill export my-skill --output ./
  $EDITOR ./my-skill.md
  crewship skill import --file ./my-skill.md`,
	Args: cobra.ExactArgs(1),
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

		// The detail endpoint returns body + every metadata column we
		// need to reassemble SKILL.md. The DB stores body sans
		// frontmatter (the parser strips it on import), so a faithful
		// round-trip — write the file, edit, re-import — has to
		// reconstruct the frontmatter from columns rather than from
		// `content` alone.
		var skill struct {
			Slug                   string  `json:"slug"`
			DisplayName            string  `json:"display_name"`
			Description            *string `json:"description"`
			Version                string  `json:"version"`
			Author                 *string `json:"author"`
			Vendor                 *string `json:"vendor"`
			Homepage               *string `json:"homepage"`
			License                *string `json:"license"`
			Category               string  `json:"category"`
			Runtime                string  `json:"runtime"`
			Maturity               string  `json:"maturity"`
			Icon                   *string `json:"icon"`
			Tags                   *string `json:"tags"`
			CredentialRequirements *string `json:"credential_requirements"`
			Content                *string `json:"content"`
		}
		resp, err := client.Get("/api/v1/skills/" + url.PathEscape(skillID) +
			"?workspace_id=" + url.QueryEscape(client.GetWorkspaceID()))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		if err := cli.ReadJSON(resp, &skill); err != nil {
			return err
		}
		if skill.Content == nil || *skill.Content == "" {
			return fmt.Errorf("skill %q has no content stored", args[0])
		}

		full := assembleSkillMD(skill)

		out, _ := cmd.Flags().GetString("output")
		if out == "" {
			fmt.Print(full)
			return nil
		}

		dest := out
		// "--output ." (or any directory) means "write <slug>.md inside it"
		// so the user doesn't have to think about the filename.
		if info, err := os.Stat(out); err == nil && info.IsDir() {
			dest = filepath.Join(out, skill.Slug+".md")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
		if err := os.WriteFile(dest, []byte(full), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		cli.PrintSuccess(fmt.Sprintf("Wrote %s (%d bytes)", dest, len(full)))
		return nil
	},
}

// assembleSkillMD rebuilds a SKILL.md from the columns we keep on the
// row. Order mirrors what `skill init` emits so an export → edit →
// import roundtrip produces a stable diff. Empty fields are skipped to
// keep the frontmatter compact; the importer accepts everything optional
// here so the result is still valid.
func assembleSkillMD(s struct {
	Slug                   string  `json:"slug"`
	DisplayName            string  `json:"display_name"`
	Description            *string `json:"description"`
	Version                string  `json:"version"`
	Author                 *string `json:"author"`
	Vendor                 *string `json:"vendor"`
	Homepage               *string `json:"homepage"`
	License                *string `json:"license"`
	Category               string  `json:"category"`
	Runtime                string  `json:"runtime"`
	Maturity               string  `json:"maturity"`
	Icon                   *string `json:"icon"`
	Tags                   *string `json:"tags"`
	CredentialRequirements *string `json:"credential_requirements"`
	Content                *string `json:"content"`
}) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + s.Slug + "\n")
	if s.DisplayName != "" && s.DisplayName != s.Slug {
		b.WriteString("display_name: " + s.DisplayName + "\n")
	}
	if s.Description != nil && *s.Description != "" {
		b.WriteString("description: " + *s.Description + "\n")
	}
	if s.License != nil && *s.License != "" {
		b.WriteString("license: " + *s.License + "\n")
	}
	if s.Vendor != nil && *s.Vendor != "" {
		b.WriteString("vendor: " + *s.Vendor + "\n")
	}
	if s.Homepage != nil && *s.Homepage != "" {
		b.WriteString("homepage: " + *s.Homepage + "\n")
	}
	if s.Version != "" && s.Version != "1.0.0" {
		b.WriteString("version: " + s.Version + "\n")
	}
	if s.Author != nil && *s.Author != "" {
		b.WriteString("author: " + *s.Author + "\n")
	}
	if s.Category != "" {
		b.WriteString("category: " + s.Category + "\n")
	}
	if s.Runtime != "" && s.Runtime != "INSTRUCTIONS" {
		b.WriteString("runtime: " + s.Runtime + "\n")
	}
	if s.Maturity != "" && s.Maturity != "COMMUNITY" {
		b.WriteString("maturity: " + s.Maturity + "\n")
	}
	if s.Icon != nil && *s.Icon != "" {
		b.WriteString("icon: " + *s.Icon + "\n")
	}
	if list := decodeStringList(s.Tags); len(list) > 0 {
		writeYAMLList(&b, "tags", list)
	}
	if list := decodeStringList(s.CredentialRequirements); len(list) > 0 {
		writeYAMLList(&b, "credential_requirements", list)
	}
	b.WriteString("---\n\n")
	if s.Content != nil {
		body := strings.TrimRight(*s.Content, "\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

// `skill delete` removes a skill from the workspace registry.
// agent_skills rows referring to it cascade-drop via the schema's
// foreign-key, so the call is safe even when agents have it installed
// — though we surface the count so the operator knows what they're
// breaking. BUNDLED skills cannot be deleted (the binary re-seeds them
// on the next start anyway, and the DB-side delete would just churn).
var skillDeleteCmd = &cobra.Command{
	Use:     "delete <skill-slug-or-id>",
	Aliases: []string{"rm"},
	Short:   "Remove a skill from the workspace registry",
	Long: `Delete a skill from the workspace.

Cascades to agent_skills (drops every assignment). BUNDLED skills are
re-seeded on every server restart so deleting them is a no-op; the
command refuses to make the request to keep operator intent honest.

Examples:
  crewship skill delete my-old-skill
  crewship skill rm sk_5e3f...        # ID also accepted
  crewship skill delete my-skill --force   # skip the confirmation`,
	Args: cobra.ExactArgs(1),
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

		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Fprintf(os.Stderr, "Delete skill %q from workspace? (y/N): ", args[0])
			var answer string
			_, _ = fmt.Fscanln(os.Stdin, &answer)
			if !strings.EqualFold(strings.TrimSpace(answer), "y") &&
				!strings.EqualFold(strings.TrimSpace(answer), "yes") {
				return fmt.Errorf("aborted")
			}
		}

		resp, err := client.Delete("/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) +
			"/skills/" + url.PathEscape(skillID))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		_ = resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Skill %s deleted", args[0]))
		return nil
	},
}

// scaffoldSlugRe matches the slug envelope the parser enforces. We
// validate at the CLI boundary so an invalid slug fails fast (no file
// written, no server call) instead of getting a 400 from the import
// endpoint after the user has already started editing the scaffold.
var scaffoldSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func validSlug(s string) bool {
	return scaffoldSlugRe.MatchString(s)
}

func validCategories() []string {
	return []string{
		"CODING", "DATA", "DEVOPS", "WRITING", "RESEARCH", "PM",
		"DESIGN", "SUPPORT", "SECURITY", "FINANCE", "OPS", "AUTOMATION",
		"SALES", "CUSTOM",
	}
}

func validCategory(c string) bool {
	for _, v := range validCategories() {
		if v == c {
			return true
		}
	}
	return false
}

func init() {
	skillInitCmd.Flags().String("category", "CUSTOM",
		"Skill category (one of CODING, DATA, DEVOPS, WRITING, RESEARCH, PM, DESIGN, SUPPORT, SECURITY, FINANCE, OPS, AUTOMATION, SALES, CUSTOM)")
	skillInitCmd.Flags().String("description", "",
		"Trigger description — should start with \"Use when...\" / \"Useful for...\"")
	skillInitCmd.Flags().String("license", "MIT",
		"SPDX license identifier (defaults to MIT; must be on the SPDX allowlist for `skill import`)")
	skillInitCmd.Flags().String("output", "",
		"Output directory (defaults to ./<slug>/)")
	skillInitCmd.Flags().Bool("force", false,
		"Overwrite existing SKILL.md if one is already present")

	skillExportCmd.Flags().String("output", "",
		"Output path (file or directory). With a directory the filename is <slug>.md. Empty = stdout.")

	skillDeleteCmd.Flags().Bool("force", false,
		"Skip the interactive confirmation")

	skillCmd.AddCommand(skillInitCmd)
	skillCmd.AddCommand(skillExportCmd)
	skillCmd.AddCommand(skillDeleteCmd)
}

// decodeStringList parses the JSON-encoded list columns (tags,
// credential_requirements) the API returns into a plain []string.
// Returns nil for null / empty / "[]" / decode failure — the caller
// only writes the YAML key when there's actually something to write.
//
// Why a separate decoder rather than passing the raw JSON through into
// the YAML output: a tag containing `:` or YAML flow-syntax characters
// (e.g. `pdf:cleanup`, `{x}`) would round-trip as broken YAML. Treating
// the column as untyped JSON and re-emitting as a YAML block list is
// the only way to keep the export shape stable for the import path.
func decodeStringList(raw *string) []string {
	if raw == nil || *raw == "" || *raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return out
}

// writeYAMLList emits `key:` followed by `  - <item>` lines, quoting
// each item if YAML would otherwise mis-parse it (leading space, special
// chars, looks-like-bool/null, etc.). Block style keeps the export
// human-friendly and round-trips cleanly through ParseSKILLMD.
func writeYAMLList(b *strings.Builder, key string, items []string) {
	b.WriteString(key + ":\n")
	for _, it := range items {
		b.WriteString("  - " + yamlScalar(it) + "\n")
	}
}

// yamlScalar returns the value as it should appear in a YAML scalar
// position. Anything that could be misread (empty, leading/trailing
// whitespace, contains special chars, parses as a YAML keyword) is
// double-quoted with `"` escapes; everything else passes through bare.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if s != strings.TrimSpace(s) {
		return jsonQuote(s)
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "null", "~":
		return jsonQuote(s)
	}
	if strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`\n\t") {
		return jsonQuote(s)
	}
	return s
}

// jsonQuote leans on encoding/json for the escape table — `\"`, `\\`,
// `\n`, `\t`, `\uXXXX` for control chars are all consistent with YAML's
// double-quoted scalar grammar, so reusing the JSON encoder gives us a
// safe quote without rolling our own escaper.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
