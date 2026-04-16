package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// BenchmarkUnblockDependentTasks exercises the dependency-status scan in
// unblockDependentTasks. Each blocked task has K deps; the last dep is PENDING
// so the function never transitions state (idempotent benchmark).
func BenchmarkUnblockDependentTasks(b *testing.B) {
	const nBlocked = 20
	const nDeps = 10

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open db: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	schema := `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT, slug TEXT);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, slug TEXT, escalation_config TEXT);
		CREATE TABLE agents (id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, name TEXT, slug TEXT,
			agent_role TEXT DEFAULT 'AGENT', lead_mode TEXT DEFAULT 'active', deleted_at TEXT);
		CREATE TABLE missions (id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, lead_agent_id TEXT,
			trace_id TEXT UNIQUE, title TEXT, description TEXT, status TEXT DEFAULT 'PLANNING',
			plan TEXT, workflow_template TEXT, total_token_count INTEGER, total_estimated_cost REAL,
			created_at TEXT, updated_at TEXT, completed_at TEXT);
		CREATE TABLE mission_tasks (id TEXT PRIMARY KEY, mission_id TEXT, assigned_agent_id TEXT,
			title TEXT, description TEXT, status TEXT DEFAULT 'PENDING', task_order INTEGER DEFAULT 0,
			depends_on TEXT DEFAULT '[]', iteration INTEGER DEFAULT 1, max_iterations INTEGER,
			result_summary TEXT, output_path TEXT, error_message TEXT, assignment_id TEXT,
			token_count INTEGER, estimated_cost REAL, started_at TEXT, completed_at TEXT,
			duration_ms INTEGER, created_at TEXT, updated_at TEXT,
			confidence REAL, needs_review INTEGER DEFAULT 0, handoff_context TEXT,
			evaluation_status TEXT, evaluation_notes TEXT,
			approval_required INTEGER DEFAULT 0, approval_status TEXT, approved_by TEXT, approved_at TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		b.Fatalf("create schema: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wsID := "ws-b"
	crewID := "crew-b"
	leadID := "agent-lead"
	agentID := "agent-worker"
	missionID := "mission-b"

	db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Bench WS', 'bench-ws')`, wsID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Bench Crew', 'bench-crew')`, crewID, wsID)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Lead', 'lead', 'LEAD')`, leadID, wsID, crewID)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Worker', 'worker', 'AGENT')`, agentID, wsID, crewID)
	db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'mission-trace-bench', 'Bench', 'IN_PROGRESS', ?, ?)`, missionID, wsID, crewID, leadID, now, now)

	// The completed trigger task that unblockDependentTasks pivots on.
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES (?, ?, ?, 'Done', 'COMPLETED', 0, '[]', ?, ?)`, "t_done", missionID, agentID, now, now)

	// Extra completed dependencies that every blocked task references.
	for i := 0; i < nDeps-2; i++ {
		id := fmt.Sprintf("t_c_%d", i)
		db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'COMPLETED', ?, '[]', ?, ?)`, id, missionID, agentID, id, i+1, now, now)
	}

	// One perpetually-PENDING dep: keeps the function idempotent across iterations.
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES (?, ?, ?, 'Pending', 'PENDING', 100, '[]', ?, ?)`, "t_pending", missionID, agentID, now, now)

	// Build deps list for blocked tasks: [t_done, t_c_0..t_c_{nDeps-3}, t_pending].
	deps := make([]string, 0, nDeps)
	deps = append(deps, "t_done")
	for i := 0; i < nDeps-2; i++ {
		deps = append(deps, fmt.Sprintf("t_c_%d", i))
	}
	deps = append(deps, "t_pending")
	depsJSON, _ := json.Marshal(deps)

	for i := 0; i < nBlocked; i++ {
		id := fmt.Sprintf("t_blocked_%d", i)
		db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'BLOCKED', ?, ?, ?, ?)`, id, missionID, agentID, id, 200+i, string(depsJSON), now, now)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.unblockDependentTasks(ctx, missionID, "t_done")
	}
}
