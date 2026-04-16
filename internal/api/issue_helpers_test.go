package api

import (
	"context"
	"database/sql"
	"testing"
)

// seedIssueFixtures inserts a workspace, owner user, crew, and lead+worker agents
// and returns useful IDs for issue/task related tests.
//
// Returns: userID, workspaceID, crewID, leadAgentID, workerAgentID.
func seedIssueFixtures(t *testing.T, db *sql.DB) (string, string, string, string, string) {
	t.Helper()

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-issue-test"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Engineering', 'eng', 'ENG')`,
		crewID, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	leadID := "agent-lead"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'Lead', 'lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		leadID, wsID, crewID); err != nil {
		t.Fatalf("insert lead agent: %v", err)
	}

	workerID := "agent-worker"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'Worker', 'worker', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		workerID, wsID, crewID); err != nil {
		t.Fatalf("insert worker agent: %v", err)
	}

	return userID, wsID, crewID, leadID, workerID
}

// seedIssue inserts a single issue (mission_type='issue') and returns its
// generated ID. Use status="BACKLOG" by default unless overridden.
func seedIssue(t *testing.T, db *sql.DB, wsID, crewID, leadID, identifier, status string) string {
	t.Helper()
	id := generateCUID()
	traceID := "trace-" + id
	if status == "" {
		status = "BACKLOG"
	}
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		    status, number, identifier, priority, sort_order, mission_type,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Test issue', ?, 1, ?, 'medium', 0, 'issue',
		    datetime('now'), datetime('now'))`,
		id, wsID, crewID, leadID, traceID, status, identifier)
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	return id
}

// seedProject inserts a project and returns its ID.
func seedProject(t *testing.T, db *sql.DB, wsID, name string) string {
	t.Helper()
	id := generateCUID()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO projects (id, workspace_id, name, slug, color, status, priority, health, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'blue', 'planned', 'none', 'on_track', datetime('now'), datetime('now'))`,
		id, wsID, name, name)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return id
}

// seedLabel inserts a workspace-scoped label and returns its ID.
func seedLabel(t *testing.T, db *sql.DB, wsID, name string) string {
	t.Helper()
	id := generateCUID()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO labels (id, workspace_id, name, color, created_at) VALUES (?, ?, ?, '#abcdef', datetime('now'))`,
		id, wsID, name)
	if err != nil {
		t.Fatalf("seed label: %v", err)
	}
	return id
}
