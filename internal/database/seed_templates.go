package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type templateStep struct {
	ID            string   `json:"id"             yaml:"id"`
	Title         string   `json:"title"          yaml:"title"`
	Description   string   `json:"description,omitempty" yaml:"description,omitempty"`
	AgentRole     string   `json:"agent_role,omitempty" yaml:"agent_role,omitempty"`
	DependsOn     []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	MaxIterations int      `json:"max_iterations,omitempty" yaml:"max_iterations,omitempty"`
	LoopBackTo    string   `json:"loop_back_to,omitempty" yaml:"loop_back_to,omitempty"`
}

type templateDef struct {
	Name        string         `json:"name"        yaml:"name"`
	Description string         `json:"description" yaml:"description"`
	Steps       []templateStep `json:"steps"       yaml:"steps"`
}

// builtinTemplateDoc is the YAML envelope for each file under
// builtin/workflow-templates/. The outer fields land directly on the
// workflow_templates row; the inner `template` block is serialised
// to JSON and stored in the template_json TEXT column.
type builtinTemplateDoc struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Icon        string      `yaml:"icon"`
	Color       string      `yaml:"color"`
	Template    templateDef `yaml:"template"`
}

//go:embed builtin/workflow-templates/*.yaml
var builtinWorkflowFS embed.FS

// loadBuiltinWorkflowTemplates parses every YAML file under the
// embedded builtin/workflow-templates/ directory into one
// builtinTemplateDoc per file. Pulled from embed.FS at process start
// rather than hardcoded in a Go literal — same set of templates, but
// authored as YAML so a non-Go contributor can add or tweak one
// without touching the codebase.
//
// Returns the docs in lexicographic filename order so the seed pass
// is deterministic (a CREATE OR IGNORE race would otherwise depend
// on map iteration order — invisible until the day a new file lands
// alphabetically before sequential.yaml).
func loadBuiltinWorkflowTemplates() ([]builtinTemplateDoc, error) {
	entries, err := builtinWorkflowFS.ReadDir("builtin/workflow-templates")
	if err != nil {
		return nil, fmt.Errorf("read embedded builtin templates dir: %w", err)
	}
	out := make([]builtinTemplateDoc, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := builtinWorkflowFS.ReadFile("builtin/workflow-templates/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}
		var doc builtinTemplateDoc
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse embedded %s: %w", e.Name(), err)
		}
		if strings.TrimSpace(doc.Name) == "" {
			return nil, fmt.Errorf("embedded %s: name is required", e.Name())
		}
		out = append(out, doc)
	}
	return out, nil
}

// SeedBuiltinTemplates inserts the built-in workflow templates for a workspace
// if they don't already exist. Called lazily on first template list access.
//
// Data source: embedded YAML files under builtin/workflow-templates/.
// The previous Go-literal source moved to YAML in iter 29 of the
// CLI hardening loop so the templates are editable without a code
// change and visible to anyone browsing the repo without parsing Go
// struct literals. Behaviour vs. the schema is unchanged — same
// INSERT OR IGNORE row shape, same is_builtin=1 marker.
func SeedBuiltinTemplates(ctx context.Context, db *sql.DB, workspaceID string, logger *slog.Logger) error {
	templates, err := loadBuiltinWorkflowTemplates()
	if err != nil {
		// Fatal at the database layer rather than letting the caller
		// receive a partially-seeded workspace — the embedded files
		// ship with the binary, so a parse failure is a build-time
		// bug, not a runtime data problem.
		return fmt.Errorf("load builtin templates: %w", err)
	}
	for _, bt := range templates {
		var exists bool
		err := db.QueryRowContext(ctx,
			`SELECT 1 FROM workflow_templates WHERE workspace_id = ? AND name = ? AND is_builtin = 1`,
			workspaceID, bt.Name).Scan(&exists)
		if err == nil {
			continue // already exists
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check builtin template %s: %w", bt.Name, err)
		}

		tmplJSON, err := json.Marshal(bt.Template)
		if err != nil {
			return fmt.Errorf("marshal template %s: %w", bt.Name, err)
		}

		id := generateSeedID("wt")
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO workflow_templates (id, workspace_id, name, description, template_json, icon, color, is_builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, workspaceID, bt.Name, bt.Description, string(tmplJSON), bt.Icon, bt.Color, now, now); err != nil {
			logger.Warn("failed to seed builtin template", "name", bt.Name, "error", err)
		}
	}
	return nil
}

func generateSeedID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%x", prefix, b)
}
