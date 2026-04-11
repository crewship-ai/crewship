package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Unassigned task (assigned_agent_id IS NULL) — should be auto-assigned
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', ?, NULL, 'Unassigned', 'PENDING', 1, '[]', ?, ?)`, missionID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)
	// Register mission as active so auto-assign failure path can broadcast
	engine.active[missionID] = &missionState{ID: missionID, CrewID: crewID, CrewSlug: "dev-crew", WorkspaceID: wsID, TraceID: "t"}

	ready, err := engine.ResolveReadyTasks(context.Background(), missionID)
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready task (auto-assigned), got %d", len(ready))
	}
	// The task should now be assigned to the worker agent (non-LEAD)
	if ready[0].AssignedAgentID == nil || *ready[0].AssignedAgentID != agentID {
		t.Errorf("expected task auto-assigned to %s, got %v", agentID, ready[0].AssignedAgentID)
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

// mockDispatcher records dispatched assignments for testing.
type mockDispatcher struct {
	dispatched []DispatchRequest
}

func (m *mockDispatcher) DispatchAssignment(_ context.Context, req DispatchRequest) error {
	m.dispatched = append(m.dispatched, req)
	return nil
}

func TestScheduleTask_CrossCrew_Connected(t *testing.T) {
	db := setupTestDB(t)
	// Add crew_connections table
	db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`)

	wsID := "ws-1"
	crewA := "crew-a"
	crewB := "crew-b"
	leadID := "agent-lead"
	crossAgentID := "agent-cross"

	db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws')`, wsID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew A', 'crew-a')`, crewA, wsID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew B', 'crew-b')`, crewB, wsID)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Lead', 'lead', 'LEAD')`, leadID, wsID, crewA)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Cross Agent', 'cross', 'AGENT')`, crossAgentID, wsID, crewB)

	// Create connection between crews
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		VALUES ('cc-1', ?, ?, ?, 'bidirectional', 'active', ?, ?)`, wsID, crewA, crewB, now, now)

	// Create mission in crew A
	missionID := "mission-cross"
	db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-cross', 'Cross Crew Mission', 'IN_PROGRESS', ?, ?)`,
		missionID, wsID, crewA, leadID, now, now)

	// Create task assigned to agent in crew B
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-cross', ?, ?, 'Cross crew task', 'PENDING', 1, '[]', ?, ?)`,
		missionID, crossAgentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	disp := &mockDispatcher{}
	engine.SetDispatcher(disp)

	ms := &missionState{
		ID:          missionID,
		CrewID:      crewA,
		CrewSlug:    "crew-a",
		LeadAgentID: leadID,
		TraceID:     "trace-cross",
		WorkspaceID: wsID,
	}

	task := TaskInfo{
		ID:              "t-cross",
		MissionID:       missionID,
		AssignedAgentID: &crossAgentID,
		Title:           "Cross crew task",
		Status:          "PENDING",
	}

	err := engine.scheduleTask(context.Background(), ms, task, nil)
	if err != nil {
		t.Fatalf("scheduleTask failed: %v", err)
	}

	// Give goroutine time to dispatch
	time.Sleep(100 * time.Millisecond)

	if len(disp.dispatched) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(disp.dispatched))
	}
	d := disp.dispatched[0]
	if d.CrewID != crewB {
		t.Errorf("expected dispatch to crew-b, got %s", d.CrewID)
	}
	if d.AgentSlug != "cross" {
		t.Errorf("expected agent slug 'cross', got %s", d.AgentSlug)
	}
}

func TestScheduleTask_CrossCrew_NotConnected(t *testing.T) {
	db := setupTestDB(t)
	db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`)

	wsID := "ws-1"
	crewA := "crew-a"
	crewB := "crew-b"
	leadID := "agent-lead"
	crossAgentID := "agent-cross"

	db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws')`, wsID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew A', 'crew-a')`, crewA, wsID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew B', 'crew-b')`, crewB, wsID)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Lead', 'lead', 'LEAD')`, leadID, wsID, crewA)
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Cross Agent', 'cross', 'AGENT')`, crossAgentID, wsID, crewB)

	// NO connection created between crews

	now := time.Now().UTC().Format(time.RFC3339)
	missionID := "mission-cross2"
	db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-cross2', 'Cross Crew Mission', 'IN_PROGRESS', ?, ?)`,
		missionID, wsID, crewA, leadID, now, now)

	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-cross2', ?, ?, 'Cross crew task', 'PENDING', 1, '[]', ?, ?)`,
		missionID, crossAgentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ms := &missionState{
		ID:          missionID,
		CrewID:      crewA,
		CrewSlug:    "crew-a",
		LeadAgentID: leadID,
		TraceID:     "trace-cross2",
		WorkspaceID: wsID,
	}

	task := TaskInfo{
		ID:              "t-cross2",
		MissionID:       missionID,
		AssignedAgentID: &crossAgentID,
		Title:           "Cross crew task",
		Status:          "PENDING",
	}

	err := engine.scheduleTask(context.Background(), ms, task, nil)
	if err == nil {
		t.Fatal("expected error for unconnected crews, got nil")
	}
	if !contains(err.Error(), "not connected") {
		t.Errorf("expected 'not connected' error, got: %s", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCircuitBreaker_TripsAfterThreshold(t *testing.T) {
	db := setupTestDB(t)
	db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`)

	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	// Simulate consecutive failures
	for i := 0; i < circuitBreakerThreshold; i++ {
		assignID := fmt.Sprintf("assign-fail-%d", i)
		taskIDStr := fmt.Sprintf("t-fail-%d", i)
		db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, started_at, created_at, updated_at)
			VALUES (?, ?, ?, 'Failing task', 'IN_PROGRESS', ?, '[]', ?, ?, ?, ?)`,
			taskIDStr, missionID, agentID, i+1, assignID, now, now, now)
		db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
			VALUES (?, ?, ?, ?, ?, 'fail task', 'FAILED', ?)`, assignID, wsID, missionID, leadID, agentID, now)

		err := engine.OnAssignmentCompleted(context.Background(), assignID, "FAILED", "", "agent crashed")
		if err != nil {
			t.Fatalf("OnAssignmentCompleted failed: %v", err)
		}
	}

	// Now try to schedule a task for the same agent -- should be blocked
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-blocked', ?, ?, 'Blocked task', 'PENDING', 10, '[]', ?, ?)`,
		missionID, agentID, now, now)

	ms := &missionState{
		ID:          missionID,
		CrewID:      crewID,
		CrewSlug:    "dev-crew",
		LeadAgentID: leadID,
		TraceID:     "trace-cb",
		WorkspaceID: wsID,
	}

	task := TaskInfo{
		ID:              "t-blocked",
		MissionID:       missionID,
		AssignedAgentID: &agentID,
		Title:           "Blocked task",
		Status:          "PENDING",
	}

	err := engine.scheduleTask(context.Background(), ms, task, nil)
	if err == nil {
		t.Fatal("expected circuit breaker error, got nil")
	}
	if !contains(err.Error(), "circuit breaker") {
		t.Errorf("expected 'circuit breaker' in error, got: %s", err)
	}
}

func TestCircuitBreaker_ResetsOnSuccess(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	// Two failures
	for i := 0; i < 2; i++ {
		assignID := fmt.Sprintf("assign-reset-%d", i)
		taskIDStr := fmt.Sprintf("t-reset-%d", i)
		db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, started_at, created_at, updated_at)
			VALUES (?, ?, ?, 'Reset task', 'IN_PROGRESS', ?, '[]', ?, ?, ?, ?)`,
			taskIDStr, missionID, agentID, i+1, assignID, now, now, now)
		db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
			VALUES (?, ?, ?, ?, ?, 'reset task', 'FAILED', ?)`, assignID, wsID, missionID, leadID, agentID, now)
		engine.OnAssignmentCompleted(context.Background(), assignID, "FAILED", "", "error")
	}

	// Check failure count is 2
	engine.cbMu.Lock()
	if engine.failures[agentID] != 2 {
		t.Errorf("expected 2 failures, got %d", engine.failures[agentID])
	}
	engine.cbMu.Unlock()

	// Success resets the counter
	successAssignID := "assign-success"
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, started_at, created_at, updated_at)
		VALUES ('t-success', ?, ?, 'Success task', 'IN_PROGRESS', 10, '[]', ?, ?, ?, ?)`,
		missionID, agentID, successAssignID, now, now, now)
	db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'success task', 'COMPLETED', ?)`, successAssignID, wsID, missionID, leadID, agentID, now)
	engine.OnAssignmentCompleted(context.Background(), successAssignID, "COMPLETED", "done", "")

	engine.cbMu.Lock()
	if engine.failures[agentID] != 0 {
		t.Errorf("expected 0 failures after success, got %d", engine.failures[agentID])
	}
	engine.cbMu.Unlock()
}

func TestOutputCompression(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	// Create assignment with a large result
	assignID := "assign-large"
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, started_at, created_at, updated_at)
		VALUES ('t-large', ?, ?, 'Large output task', 'IN_PROGRESS', 1, '[]', ?, ?, ?, ?)`,
		missionID, agentID, assignID, now, now, now)
	db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'large task', 'COMPLETED', ?)`, assignID, wsID, missionID, leadID, agentID, now)

	// Create a result larger than maxResultSummaryLen
	largeResult := ""
	for i := 0; i < maxResultSummaryLen+1000; i++ {
		largeResult += "x"
	}

	engine.OnAssignmentCompleted(context.Background(), assignID, "COMPLETED", largeResult, "")

	// Verify it was truncated in DB
	var stored string
	db.QueryRow(`SELECT result_summary FROM mission_tasks WHERE id = 't-large'`).Scan(&stored)
	if len(stored) > maxResultSummaryLen+50 {
		t.Errorf("expected truncated result, got length %d", len(stored))
	}
	if !contains(stored, "truncated") {
		t.Error("expected truncation marker in stored result")
	}
}

// --- Approval Gate Tests ---

func TestCheckApprovalGate_ExplicitFlag(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, approval_required, created_at, updated_at)
		VALUES ('t-approval', ?, ?, 'Task Requiring Approval', 'COMPLETED', 1, '[]', 1, ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	status := engine.checkApprovalGate(context.Background(), "t-approval", missionID)
	if status != "AWAITING_APPROVAL" {
		t.Errorf("expected AWAITING_APPROVAL, got %s", status)
	}
}

func TestCheckApprovalGate_NoFlag_NoConfig(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-normal', ?, ?, 'Normal Task', 'COMPLETED', 1, '[]', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	status := engine.checkApprovalGate(context.Background(), "t-normal", missionID)
	if status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", status)
	}
}

func TestCheckApprovalGate_ConfidenceThreshold(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)

	cfg, _ := json.Marshal(EscalationConfig{
		AutoApproveThreshold: 0.9,
		RequireApprovalBelow: 0.5,
		NotifyThreshold:      0.7,
	})
	db.Exec(`UPDATE crews SET escalation_config = ? WHERE id = ?`, string(cfg), crewID)

	missionID := createTestMission(t, db, wsID, crewID, leadID)
	now := time.Now().UTC().Format(time.RFC3339)

	tests := []struct {
		id         string
		confidence float64
		expected   string
	}{
		{"t-high", 0.95, "COMPLETED"},       // above auto-approve
		{"t-low", 0.3, "AWAITING_APPROVAL"}, // below require_approval
		{"t-mid", 0.6, "COMPLETED"},         // between thresholds (notify only)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	for _, tt := range tests {
		db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, confidence, created_at, updated_at)
			VALUES (?, ?, ?, 'Task', 'COMPLETED', 1, '[]', ?, ?, ?)`,
			tt.id, missionID, agentID, tt.confidence, now, now)

		status := engine.checkApprovalGate(context.Background(), tt.id, missionID)
		if status != tt.expected {
			t.Errorf("%s: confidence=%.2f, expected %s, got %s", tt.id, tt.confidence, tt.expected, status)
		}
	}
}

func TestApproveTask_Approve(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-await', ?, ?, 'Awaiting Task', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	err := engine.ApproveTask(context.Background(), "t-await", "user-1", true, "looks good")
	if err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}

	var status, approvalStatus, approvedBy string
	db.QueryRow(`SELECT status, approval_status, approved_by FROM mission_tasks WHERE id = 't-await'`).Scan(&status, &approvalStatus, &approvedBy)
	if status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", status)
	}
	if approvalStatus != "APPROVED" {
		t.Errorf("expected APPROVED, got %s", approvalStatus)
	}
	if approvedBy != "user-1" {
		t.Errorf("expected user-1, got %s", approvedBy)
	}
}

func TestApproveTask_Reject_CascadesFailure(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-parent', ?, ?, 'Parent', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`,
		missionID, agentID, now, now)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-child', ?, ?, 'Child', 'BLOCKED', 2, '["t-parent"]', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	err := engine.ApproveTask(context.Background(), "t-parent", "user-1", false, "rejected")
	if err != nil {
		t.Fatalf("ApproveTask reject: %v", err)
	}

	var parentStatus, childStatus, childErr string
	db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-parent'`).Scan(&parentStatus)
	db.QueryRow(`SELECT status, COALESCE(error_message, '') FROM mission_tasks WHERE id = 't-child'`).Scan(&childStatus, &childErr)

	if parentStatus != "FAILED" {
		t.Errorf("parent: expected FAILED, got %s", parentStatus)
	}
	if childStatus != "FAILED" {
		t.Errorf("child: expected FAILED (cascade), got %s", childStatus)
	}
	if childErr == "" {
		t.Error("child: expected error_message from cascade")
	}
}

func TestApproveTask_DoubleApprove_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-double', ?, ?, 'Double', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	// First approve succeeds
	if err := engine.ApproveTask(context.Background(), "t-double", "user-1", true, ""); err != nil {
		t.Fatalf("first approve: %v", err)
	}

	// Second approve should fail (task is no longer AWAITING_APPROVAL)
	err := engine.ApproveTask(context.Background(), "t-double", "user-2", true, "")
	if err == nil {
		t.Error("expected error on double approve, got nil")
	}
}

func TestApproveTask_WrongStatus_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-pending', ?, ?, 'Pending', 'PENDING', 1, '[]', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	err := engine.ApproveTask(context.Background(), "t-pending", "user-1", true, "")
	if err == nil {
		t.Error("expected error when approving non-AWAITING_APPROVAL task")
	}
}

func TestCheckApprovalGate_AutoApproveOverridesFlag(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)

	cfg, _ := json.Marshal(EscalationConfig{AutoApproveThreshold: 0.8})
	db.Exec(`UPDATE crews SET escalation_config = ? WHERE id = ?`, string(cfg), crewID)

	missionID := createTestMission(t, db, wsID, crewID, leadID)
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, approval_required, confidence, created_at, updated_at)
		VALUES ('t-override', ?, ?, 'Override', 'COMPLETED', 1, '[]', 1, 0.95, ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)

	status := engine.checkApprovalGate(context.Background(), "t-override", missionID)
	if status != "COMPLETED" {
		t.Errorf("expected auto-approve to override flag, got %s", status)
	}
}
