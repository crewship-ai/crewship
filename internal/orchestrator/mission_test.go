package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	schema := `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT, slug TEXT);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, slug TEXT);
		CREATE TABLE agents (id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, name TEXT, slug TEXT,
			agent_role TEXT DEFAULT 'AGENT', deleted_at TEXT);
		CREATE TABLE missions (id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, lead_agent_id TEXT,
			trace_id TEXT UNIQUE, title TEXT, description TEXT, status TEXT DEFAULT 'PLANNING',
			plan TEXT, workflow_template TEXT, total_token_count INTEGER, total_estimated_cost REAL,
			created_at TEXT, updated_at TEXT, completed_at TEXT);
		CREATE TABLE mission_tasks (id TEXT PRIMARY KEY, mission_id TEXT, assigned_agent_id TEXT,
			title TEXT, description TEXT, status TEXT DEFAULT 'PENDING', task_order INTEGER DEFAULT 0,
			depends_on TEXT DEFAULT '[]', iteration INTEGER DEFAULT 1, max_iterations INTEGER,
			result_summary TEXT, output_path TEXT, error_message TEXT, assignment_id TEXT,
			token_count INTEGER, estimated_cost REAL, started_at TEXT, completed_at TEXT,
			duration_ms INTEGER, created_at TEXT, updated_at TEXT);
		CREATE TABLE assignments (id TEXT PRIMARY KEY, workspace_id TEXT, chat_id TEXT,
			assigned_by_id TEXT, assigned_to_id TEXT, task TEXT, status TEXT DEFAULT 'PENDING',
			started_at TEXT, finished_at TEXT, result_summary TEXT, error_message TEXT,
			group_id TEXT, created_at TEXT);
		CREATE TABLE chats (id TEXT PRIMARY KEY, agent_id TEXT, workspace_id TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	return db
}

func seedTestData(t *testing.T, db *sql.DB) (workspaceID, crewID, leadID, agentID string) {
	t.Helper()
	workspaceID = "ws-1"
	crewID = "crew-1"
	leadID = "agent-lead"
	agentID = "agent-worker"

	db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Test WS', 'test-ws')`, workspaceID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Dev Crew', 'dev-crew')`, crewID, workspaceID)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Lead Anna', 'anna', 'LEAD')`, leadID, workspaceID, crewID)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Worker Bob', 'bob', 'AGENT')`, agentID, workspaceID, crewID)

	return
}

func createTestMission(t *testing.T, db *sql.DB, wsID, crewID, leadID string) string {
	t.Helper()
	missionID := "mission-1"
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'mission-trace-1', 'Test Mission', 'IN_PROGRESS', ?, ?)`,
		missionID, wsID, crewID, leadID, now, now)
	return missionID
}

func TestResolveReadyTasks_NoDeps(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Task 1', 'PENDING', 1, '[]', ?, ?)`, missionID, agentID, now, now)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', ?, ?, 'Task 2', 'PENDING', 2, '[]', ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ready, err := engine.ResolveReadyTasks(context.Background(), missionID)
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 2 {
		t.Errorf("expected 2 ready tasks, got %d", len(ready))
	}
}

func TestResolveReadyTasks_WithDeps(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Fetch data', 'COMPLETED', 1, '[]', ?, ?)`, missionID, agentID, now, now)

	deps, _ := json.Marshal([]string{"t1"})
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', ?, ?, 'Process data', 'PENDING', 2, ?, ?, ?)`, missionID, agentID, string(deps), now, now)

	deps2, _ := json.Marshal([]string{"t1", "t2"})
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t3', ?, ?, 'Write report', 'BLOCKED', 3, ?, ?, ?)`, missionID, agentID, string(deps2), now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ready, err := engine.ResolveReadyTasks(context.Background(), missionID)
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task, got %d", len(ready))
	}
	if ready[0].ID != "t2" {
		t.Errorf("expected t2 to be ready, got %s", ready[0].ID)
	}
}

func TestResolveReadyTasks_UnassignedSkipped(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, _ := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Unassigned task (assigned_agent_id IS NULL)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, NULL, 'Unassigned', 'PENDING', 1, '[]', ?, ?)`, missionID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ready, err := engine.ResolveReadyTasks(context.Background(), missionID)
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("expected 0 ready tasks (unassigned), got %d", len(ready))
	}
}

func TestCheckMissionCompletion_AllDone(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Task 1', 'COMPLETED', 1, '[]', ?, ?)`, missionID, agentID, now, now)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', ?, ?, 'Task 2', 'COMPLETED', 2, '[]', ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ms := &missionState{ID: missionID, CrewID: crewID, CrewSlug: "dev-crew", WorkspaceID: wsID, TraceID: "mission-trace-1"}
	if err := engine.checkMissionCompletion(context.Background(), ms); err != nil {
		t.Fatalf("checkMissionCompletion: %v", err)
	}

	var status string
	db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status)
	if status != "REVIEW" {
		t.Errorf("expected mission status REVIEW, got %s", status)
	}
}

func TestCheckMissionCompletion_WithFailure(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Task 1', 'COMPLETED', 1, '[]', ?, ?)`, missionID, agentID, now, now)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', ?, ?, 'Task 2', 'FAILED', 2, '[]', ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ms := &missionState{ID: missionID, CrewID: crewID, CrewSlug: "dev-crew", WorkspaceID: wsID, TraceID: "mission-trace-1"}
	if err := engine.checkMissionCompletion(context.Background(), ms); err != nil {
		t.Fatalf("checkMissionCompletion: %v", err)
	}

	var status string
	db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status)
	if status != "FAILED" {
		t.Errorf("expected mission status FAILED, got %s", status)
	}
}

func TestCheckMissionCompletion_StillRunning(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Task 1', 'COMPLETED', 1, '[]', ?, ?)`, missionID, agentID, now, now)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', ?, ?, 'Task 2', 'IN_PROGRESS', 2, '[]', ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ms := &missionState{ID: missionID, CrewID: crewID, CrewSlug: "dev-crew", WorkspaceID: wsID, TraceID: "mission-trace-1"}
	if err := engine.checkMissionCompletion(context.Background(), ms); err != nil {
		t.Fatalf("checkMissionCompletion: %v", err)
	}

	var status string
	db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status)
	if status != "IN_PROGRESS" {
		t.Errorf("expected mission status IN_PROGRESS (unchanged), got %s", status)
	}
}

func TestOnAssignmentCompleted(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Task 1', 'IN_PROGRESS', 1, '[]', 'assign-1', ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	// Register mission as active so broadcasts work
	engine.mu.Lock()
	engine.active[missionID] = &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		WorkspaceID: wsID, TraceID: "mission-trace-1",
		cancel: func() {},
	}
	engine.mu.Unlock()

	err := engine.OnAssignmentCompleted(context.Background(), "assign-1", "COMPLETED", "Done successfully", "")
	if err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}

	var status, result string
	db.QueryRow(`SELECT status, COALESCE(result_summary, '') FROM mission_tasks WHERE id = 't1'`).Scan(&status, &result)
	if status != "COMPLETED" {
		t.Errorf("expected task status COMPLETED, got %s", status)
	}
	if result != "Done successfully" {
		t.Errorf("expected result 'Done successfully', got %q", result)
	}
}

func TestOnAssignmentCompleted_NoMatch(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	err := engine.OnAssignmentCompleted(context.Background(), "nonexistent", "COMPLETED", "", "")
	if err != nil {
		t.Errorf("expected nil error for unlinked assignment, got %v", err)
	}
}

func TestUnblockDependentTasks(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Fetch', 'COMPLETED', 1, '[]', ?, ?)`, missionID, agentID, now, now)

	deps, _ := json.Marshal([]string{"t1"})
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', ?, ?, 'Process', 'BLOCKED', 2, ?, ?, ?)`, missionID, agentID, string(deps), now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	engine.mu.Lock()
	engine.active[missionID] = &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		WorkspaceID: wsID, TraceID: "mission-trace-1",
		cancel: func() {},
	}
	engine.mu.Unlock()

	engine.unblockDependentTasks(context.Background(), missionID, "t1")

	var status string
	db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't2'`).Scan(&status)
	if status != "PENDING" {
		t.Errorf("expected t2 status PENDING (unblocked), got %s", status)
	}
}

func TestParseDependsOn(t *testing.T) {
	tests := []struct {
		input    string
		expected int
		hasErr   bool
	}{
		{"", 0, false},
		{"[]", 0, false},
		{`["t1"]`, 1, false},
		{`["t1","t2","t3"]`, 3, false},
		{"invalid", 0, true},
	}

	for _, tc := range tests {
		deps, err := parseDependsOn(tc.input)
		if tc.hasErr && err == nil {
			t.Errorf("parseDependsOn(%q): expected error", tc.input)
		}
		if !tc.hasErr && err != nil {
			t.Errorf("parseDependsOn(%q): unexpected error: %v", tc.input, err)
		}
		if len(deps) != tc.expected {
			t.Errorf("parseDependsOn(%q): expected %d deps, got %d", tc.input, tc.expected, len(deps))
		}
	}
}

func TestProgressWriter(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	pw := NewProgressWriter()
	pw.WriteEvent("trace-1", "test-crew", ProgressEvent{
		Type:      "mission_started",
		MissionID: "m-1",
	})
	pw.WriteEvent("trace-1", "test-crew", ProgressEvent{
		Type:      "task_started",
		TaskID:    "t-1",
		AgentSlug: "bob",
		Title:     "Fetch data",
	})
	pw.WriteEvent("trace-1", "test-crew", ProgressEvent{
		Type:      "task_completed",
		TaskID:    "t-1",
		AgentSlug: "bob",
		Title:     "Fetch data",
		Summary:   "Downloaded 1000 records",
	})

	events, err := pw.ReadProgress("trace-1", "test-crew")
	if err != nil {
		t.Fatalf("ReadProgress: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Type != "mission_started" {
		t.Errorf("expected first event type 'mission_started', got %q", events[0].Type)
	}
	if events[2].Summary != "Downloaded 1000 records" {
		t.Errorf("expected summary, got %q", events[2].Summary)
	}

	ctx := pw.BuildProgressContext("trace-1", "test-crew")
	if ctx == "" {
		t.Error("BuildProgressContext returned empty string")
	}
}
