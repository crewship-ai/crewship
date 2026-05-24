package database

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CrewTemplateAgent defines an agent within a crew template.
type CrewTemplateAgent struct {
	Name         string   `json:"name"          yaml:"name"`
	Slug         string   `json:"slug"          yaml:"slug"`
	RoleTitle    string   `json:"role_title"    yaml:"role_title"`
	AgentRole    string   `json:"agent_role"    yaml:"agent_role"`
	CLIAdapter   string   `json:"cli_adapter"   yaml:"cli_adapter"`
	LLMProvider  string   `json:"llm_provider"  yaml:"llm_provider"`
	LLMModel     string   `json:"llm_model"     yaml:"llm_model"`
	ToolProfile  string   `json:"tool_profile"  yaml:"tool_profile"`
	SystemPrompt string   `json:"system_prompt" yaml:"system_prompt"`
	Skills       []string `json:"skills,omitempty" yaml:"skills,omitempty"`
}

// builtinCrewTemplateDoc is the on-disk shape of a builtin crew
// template YAML file under builtin/crew-templates/. Each file
// produces one row in the crew_templates table (with is_builtin=1).
type builtinCrewTemplateDoc struct {
	Name        string              `yaml:"name"`
	Slug        string              `yaml:"slug"`
	Description string              `yaml:"description"`
	Icon        string              `yaml:"icon"`
	Color       string              `yaml:"color"`
	Category    string              `yaml:"category"`
	Agents      []CrewTemplateAgent `yaml:"agents"`
}

//go:embed builtin/crew-templates/*.yaml
var builtinCrewTemplateFS embed.FS

// loadBuiltinCrewTemplates parses every YAML file under the embedded
// builtin/crew-templates/ directory into one builtinCrewTemplateDoc
// per file. Same pattern as loadBuiltinWorkflowTemplates — embed.FS
// at build time, deterministic lexicographic order, fatal on parse
// failure since the files ship with the binary.
//
// Migrated from a Go struct literal (formerly ~380 lines of
// builtinCrewTemplates) to per-template YAML files in iter F2-5 so
// the catalogue is editable without a code change and visible to
// anyone browsing the repo without parsing Go syntax. The on-disk
// crew_templates row shape, is_builtin=1 marker, and update-or-
// insert behaviour are unchanged — a migrated template is
// byte-identical to its pre-migration form once SeedBuiltinCrewTemplates
// runs.
func loadBuiltinCrewTemplates() ([]builtinCrewTemplateDoc, error) {
	entries, err := builtinCrewTemplateFS.ReadDir("builtin/crew-templates")
	if err != nil {
		return nil, fmt.Errorf("read embedded builtin crew-templates dir: %w", err)
	}
	out := make([]builtinCrewTemplateDoc, 0, len(entries))
	seenSlugs := make(map[string]string, len(entries)) // slug → originating filename
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := builtinCrewTemplateFS.ReadFile("builtin/crew-templates/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}
		var doc builtinCrewTemplateDoc
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse embedded %s: %w", e.Name(), err)
		}
		// Full required-field validation — yaml.Unmarshal happily
		// produces zero values on schema drift; the later
		// SELECT-or-INSERT by slug would silently swallow a
		// template that lost its Name or has a typo in a field.
		if strings.TrimSpace(doc.Slug) == "" {
			return nil, fmt.Errorf("embedded %s: slug is required", e.Name())
		}
		if strings.TrimSpace(doc.Name) == "" {
			return nil, fmt.Errorf("embedded %s: name is required", e.Name())
		}
		if strings.TrimSpace(doc.Description) == "" {
			return nil, fmt.Errorf("embedded %s: description is required", e.Name())
		}
		if strings.TrimSpace(doc.Icon) == "" {
			return nil, fmt.Errorf("embedded %s: icon is required", e.Name())
		}
		if strings.TrimSpace(doc.Color) == "" {
			return nil, fmt.Errorf("embedded %s: color is required", e.Name())
		}
		if strings.TrimSpace(doc.Category) == "" {
			return nil, fmt.Errorf("embedded %s: category is required", e.Name())
		}
		if len(doc.Agents) == 0 {
			return nil, fmt.Errorf("embedded %s: agents is required (zero agents)", e.Name())
		}
		// Duplicate-slug detection: SeedBuiltinCrewTemplates upserts
		// by slug + is_builtin=1, so two YAML files with the same
		// slug would silently collapse to one row.
		if prior, ok := seenSlugs[doc.Slug]; ok {
			return nil, fmt.Errorf("embedded %s: duplicate template slug %q (already in %s)", e.Name(), doc.Slug, prior)
		}
		seenSlugs[doc.Slug] = e.Name()
		out = append(out, doc)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("embedded builtin/crew-templates/: zero templates loaded — the directory must ship at least one .yaml file")
	}
	return out, nil
}

// SeedBuiltinCrewTemplates inserts bundled crew templates and updates existing
// builtin rows so format changes (emoji → lucide, hex → palette ID, agent
// roster tweaks) propagate to dev / prod DBs that ran an earlier seed. Custom
// user templates (is_builtin=0) with conflicting slug are NEVER touched.
//
// Data source: embedded YAML files under builtin/crew-templates/.
// Same lifecycle as SeedBuiltinTemplates (workflow templates) —
// updates first (touches only is_builtin=1), inserts if no row
// existed.
func SeedBuiltinCrewTemplates(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	templates, err := loadBuiltinCrewTemplates()
	if err != nil {
		// Fatal at the database layer rather than letting the caller
		// receive a partially-seeded workspace — the embedded files
		// ship with the binary, so a parse failure is a build-time
		// bug, not a runtime data problem.
		return fmt.Errorf("load builtin crew templates: %w", err)
	}
	for _, bt := range templates {
		agentsJSON, err := json.Marshal(bt.Agents)
		if err != nil {
			return fmt.Errorf("marshal agents for %s: %w", bt.Slug, err)
		}

		now := time.Now().UTC().Format(time.RFC3339)

		// Try update first — touches only builtin rows; rowsAffected=0 means we
		// need to insert. Avoids ON CONFLICT(slug) which would also update a
		// user-created row that happened to share the slug.
		res, err := db.ExecContext(ctx, `
			UPDATE crew_templates
			SET name = ?, description = ?, icon = ?, color = ?,
			    category = ?, agents_json = ?, updated_at = ?
			WHERE slug = ? AND is_builtin = 1`,
			bt.Name, bt.Description, bt.Icon, bt.Color, bt.Category, string(agentsJSON), now, bt.Slug)
		if err != nil {
			logger.Warn("failed to update builtin crew template", "slug", bt.Slug, "error", err)
			continue
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			continue
		}

		id := generateSeedID("ct")
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO crew_templates (id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, bt.Name, bt.Slug, bt.Description, bt.Icon, bt.Color, bt.Category, string(agentsJSON), now, now); err != nil {
			logger.Warn("failed to seed crew template", "slug", bt.Slug, "error", err)
		}
	}
	return nil
}
