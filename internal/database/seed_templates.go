package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

type templateStep struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	AgentRole     string   `json:"agent_role,omitempty"`
	DependsOn     []string `json:"depends_on,omitempty"`
	MaxIterations int      `json:"max_iterations,omitempty"`
	LoopBackTo    string   `json:"loop_back_to,omitempty"`
}

type templateDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []templateStep `json:"steps"`
}

var builtinTemplates = []struct {
	name        string
	description string
	icon        string
	color       string
	tmpl        templateDef
}{
	{
		name: "sequential", description: "Tasks execute one after another in order",
		icon: "arrow-right", color: "#3b82f6",
		tmpl: templateDef{
			Name: "sequential", Description: "Tasks execute one after another in order",
			Steps: []templateStep{
				{ID: "step-1", Title: "Step 1", AgentRole: "agent"},
				{ID: "step-2", Title: "Step 2", AgentRole: "agent", DependsOn: []string{"step-1"}},
				{ID: "step-3", Title: "Step 3", AgentRole: "agent", DependsOn: []string{"step-2"}},
			},
		},
	},
	{
		name: "parallel", description: "All tasks run simultaneously, then results are aggregated",
		icon: "git-branch", color: "#22c55e",
		tmpl: templateDef{
			Name: "parallel", Description: "All tasks run simultaneously, then results are aggregated",
			Steps: []templateStep{
				{ID: "task-a", Title: "Parallel Task A", AgentRole: "agent"},
				{ID: "task-b", Title: "Parallel Task B", AgentRole: "agent"},
				{ID: "task-c", Title: "Parallel Task C", AgentRole: "agent"},
				{ID: "aggregate", Title: "Aggregate Results", AgentRole: "lead", DependsOn: []string{"task-a", "task-b", "task-c"}},
			},
		},
	},
	{
		name: "dev-test-loop", description: "Developer writes code, tester reviews. On failure, loops back (max 3 iterations)",
		icon: "repeat", color: "#f59e0b",
		tmpl: templateDef{
			Name: "dev-test-loop", Description: "Developer writes code, tester reviews. On failure, loops back to developer (max 3 iterations)",
			Steps: []templateStep{
				{ID: "develop", Title: "Implement feature", AgentRole: "developer", MaxIterations: 3},
				{ID: "test", Title: "Test and review", AgentRole: "tester", DependsOn: []string{"develop"}, LoopBackTo: "develop", MaxIterations: 3},
			},
		},
	},
	{
		name: "pipeline", description: "Sequential stage followed by parallel tasks, then final aggregation",
		icon: "git-merge", color: "#8b5cf6",
		tmpl: templateDef{
			Name: "pipeline", Description: "Sequential stage followed by parallel tasks, then final aggregation",
			Steps: []templateStep{
				{ID: "prepare", Title: "Prepare", AgentRole: "agent"},
				{ID: "work-a", Title: "Work Stream A", AgentRole: "agent", DependsOn: []string{"prepare"}},
				{ID: "work-b", Title: "Work Stream B", AgentRole: "agent", DependsOn: []string{"prepare"}},
				{ID: "finalize", Title: "Finalize", AgentRole: "lead", DependsOn: []string{"work-a", "work-b"}},
			},
		},
	},
}

// SeedBuiltinTemplates inserts the built-in workflow templates for a workspace
// if they don't already exist. Called lazily on first template list access.
func SeedBuiltinTemplates(ctx context.Context, db *sql.DB, workspaceID string, logger *slog.Logger) error {
	for _, bt := range builtinTemplates {
		var exists bool
		err := db.QueryRowContext(ctx,
			`SELECT 1 FROM workflow_templates WHERE workspace_id = ? AND name = ? AND is_builtin = 1`,
			workspaceID, bt.name).Scan(&exists)
		if err == nil {
			continue // already exists
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check builtin template %s: %w", bt.name, err)
		}

		tmplJSON, err := json.Marshal(bt.tmpl)
		if err != nil {
			return fmt.Errorf("marshal template %s: %w", bt.name, err)
		}

		id := generateSeedID("wt")
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO workflow_templates (id, workspace_id, name, description, template_json, icon, color, is_builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, workspaceID, bt.name, bt.description, string(tmplJSON), bt.icon, bt.color, now, now); err != nil {
			logger.Warn("failed to seed builtin template", "name", bt.name, "error", err)
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
