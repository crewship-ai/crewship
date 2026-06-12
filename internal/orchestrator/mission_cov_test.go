package orchestrator

// Coverage tests for mission.go: generateID, loadTasks error path, and the
// runMissionLoop branches (deadline cleanup, terminal-status exit, lead
// planning dispatch, deadlock detection). Includes covMissionDB — an
// extended in-memory schema with the tables the completion/notification
// paths touch (mission_comments, mission_activity, inbox_items,
// crew_connections) that the original setupTestDB omits.

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"
)

// covMissionDB builds an extended schema superset of setupTestDB so that
// inbox notifications, comments, activity, and cross-crew checks all have
// real tables to land in.
func covMissionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// A pooled :memory: database is per-connection — a second pooled
	// connection sees a fresh empty schema. Tests here touch the DB from
	// dispatcher goroutines, so pin the pool to one shared connection.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	schema := `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT, slug TEXT);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, slug TEXT, escalation_config TEXT);
		CREATE TABLE agents (id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, name TEXT, slug TEXT,
			agent_role TEXT DEFAULT 'AGENT', lead_mode TEXT DEFAULT 'active', deleted_at TEXT);
		CREATE TABLE missions (id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, lead_agent_id TEXT,
			trace_id TEXT UNIQUE, title TEXT, description TEXT, status TEXT DEFAULT 'PLANNING',
			identifier TEXT, mission_type TEXT,
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
		CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT, email TEXT);
		CREATE TABLE crew_connections (id TEXT PRIMARY KEY, from_crew_id TEXT, to_crew_id TEXT,
			status TEXT, direction TEXT);
		CREATE TABLE mission_comments (id TEXT PRIMARY KEY, mission_id TEXT, author_type TEXT,
			author_id TEXT, body TEXT, created_at TEXT, updated_at TEXT);
		CREATE TABLE mission_activity (id TEXT PRIMARY KEY, mission_id TEXT, actor_type TEXT,
			actor_id TEXT, action TEXT, details TEXT, created_at TEXT);
		CREATE TABLE inbox_items (id TEXT PRIMARY KEY, workspace_id TEXT, kind TEXT, source_id TEXT,
			target_user_id TEXT, target_role TEXT, title TEXT, body_md TEXT,
			sender_type TEXT, sender_id TEXT, sender_name TEXT,
			state TEXT, priority TEXT, blocking INTEGER, payload_json TEXT,
			created_at TEXT, updated_at TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create cov schema: %v", err)
	}
	return db
}

// covSeed inserts the standard ws/crew/lead/worker rows.
func covSeed(t *testing.T, db *sql.DB) (wsID, crewID, leadID, workerID string) {
	t.Helper()
	wsID, crewID, leadID, workerID = "ws-1", "crew-1", "agent-lead", "agent-worker"
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws')`, wsID)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', 'dev-crew')`, crewID, wsID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Anna', 'anna', 'LEAD')`, leadID, wsID, crewID)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Bob', 'bob', 'AGENT')`, workerID, wsID, crewID)
	return
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// covMission inserts a mission row and returns its missionState.
func covMission(t *testing.T, db *sql.DB, id, status string) *missionState {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, 'ws-1', 'crew-1', 'agent-lead', ?, 'Cov Mission', ?, ?, ?)`,
		id, "trace-"+id, status, now, now)
	return &missionState{
		ID: id, Title: "Cov Mission", CrewID: "crew-1", CrewSlug: "dev-crew",
		LeadAgentID: "agent-lead", TraceID: "trace-" + id, WorkspaceID: "ws-1",
		cancel: func() {},
	}
}

func covTaskStatus(t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = ?`, taskID).Scan(&s); err != nil {
		t.Fatalf("task %s status: %v", taskID, err)
	}
	return s
}

func covMissionStatus(t *testing.T, db *sql.DB, missionID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&s); err != nil {
		t.Fatalf("mission %s status: %v", missionID, err)
	}
	return s
}

// covDispatcher records dispatch requests on a channel and returns err.
type covDispatcher struct {
	mu  sync.Mutex
	ch  chan DispatchRequest
	err error
}

func newCovDispatcher(err error) *covDispatcher {
	return &covDispatcher{ch: make(chan DispatchRequest, 8), err: err}
}

func (d *covDispatcher) DispatchAssignment(_ context.Context, r DispatchRequest) error {
	d.mu.Lock()
	err := d.err
	d.mu.Unlock()
	d.ch <- r
	return err
}

// ---- generateID ----

func TestGenerateID_FormatAndUniqueness(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateID()
		if !strings.HasPrefix(id, "m_") {
			t.Fatalf("id %q must have m_ prefix", id)
		}
		if len(id) <= 4 {
			t.Fatalf("id %q too short", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = true
	}
}

// ---- loadTasks error path ----

func TestLoadTasks_QueryErrorOnClosedDB(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	e := newLifecycleEngine(t, db)
	db.Close()
	_, err := e.loadTasks(context.Background(), "m1")
	if err == nil || !strings.Contains(err.Error(), "query tasks") {
		t.Fatalf("expected query tasks error, got %v", err)
	}
}

// ---- runMissionLoop ----

func TestRunMissionLoop_DeadlineCleanupFailsMissionAndApprovalTasks(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	e := newLifecycleEngine(t, db)
	ms := covMission(t, db, "m-timeout", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-appr', 'm-timeout', 'agent-worker', 'Held', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`, now, now)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	ms.cancel = cancel
	e.mu.Lock()
	e.active[ms.ID] = ms
	e.mu.Unlock()

	e.runMissionLoop(ctx, ms) // returns immediately — deadline already exceeded

	if got := covMissionStatus(t, db, "m-timeout"); got != "FAILED" {
		t.Errorf("mission status = %q, want FAILED after timeout", got)
	}
	if got := covTaskStatus(t, db, "t-appr"); got != "FAILED" {
		t.Errorf("awaiting-approval task = %q, want FAILED", got)
	}
	var errMsg string
	db.QueryRow(`SELECT error_message FROM mission_tasks WHERE id = 't-appr'`).Scan(&errMsg)
	if errMsg != "mission timed out" {
		t.Errorf("error_message = %q, want 'mission timed out'", errMsg)
	}
	e.mu.Lock()
	_, stillActive := e.active[ms.ID]
	e.mu.Unlock()
	if stillActive {
		t.Error("mission must be removed from active map after loop exit")
	}
}

func TestRunMissionLoop_ExitsWhenMissionNotInProgress(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	e := newLifecycleEngine(t, db)
	ms := covMission(t, db, "m-done", "COMPLETED")
	e.mu.Lock()
	e.active[ms.ID] = ms
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ms.cancel = cancel

	start := time.Now()
	e.runMissionLoop(ctx, ms) // must return on the first 3s tick, not at the 10s deadline
	if elapsed := time.Since(start); elapsed >= 9*time.Second {
		t.Fatalf("loop did not exit at first tick for terminal mission (took %v)", elapsed)
	}
	if got := covMissionStatus(t, db, "m-done"); got != "COMPLETED" {
		t.Errorf("terminal mission status must be untouched, got %q", got)
	}
}

func TestRunMissionLoop_DeadlockFailsMission(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	e := newLifecycleEngine(t, db)
	ms := covMission(t, db, "m-deadlock", "IN_PROGRESS")
	ms.planningDispatched = true // skip lead planning branch
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-failed', 'm-deadlock', 'agent-worker', 'Broke', 'FAILED', 1, '[]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-blocked', 'm-deadlock', 'agent-worker', 'Stuck', 'BLOCKED', 2, '["t-failed"]', ?, ?)`, now, now)

	e.mu.Lock()
	e.active[ms.ID] = ms
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ms.cancel = cancel

	start := time.Now()
	e.runMissionLoop(ctx, ms)
	if elapsed := time.Since(start); elapsed >= 9*time.Second {
		t.Fatalf("loop did not exit on deadlock detection (took %v)", elapsed)
	}
	if got := covMissionStatus(t, db, "m-deadlock"); got != "FAILED" {
		t.Errorf("deadlocked mission status = %q, want FAILED", got)
	}
}

func TestRunMissionLoop_DispatchesLeadPlanningWhenNoTasks(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	e := newLifecycleEngine(t, db)
	d := newCovDispatcher(nil)
	e.SetDispatcher(d)

	ms := covMission(t, db, "m-plan", "IN_PROGRESS")
	mustExec(t, db, `UPDATE missions SET description = 'Build the thing' WHERE id = 'm-plan'`)
	e.mu.Lock()
	e.active[ms.ID] = ms
	e.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ms.cancel = cancel

	done := make(chan struct{})
	go func() { e.runMissionLoop(ctx, ms); close(done) }()

	select {
	case req := <-d.ch:
		if !req.LeadPlanning {
			t.Errorf("planning dispatch must set LeadPlanning, got %+v", req)
		}
		if req.AgentSlug != "anna" || req.AgentID != "agent-lead" {
			t.Errorf("planning must go to the lead: %+v", req)
		}
		if !strings.Contains(req.Task, "[MISSION PLANNING REQUEST]") {
			t.Errorf("planning prompt missing header: %.120s", req.Task)
		}
		if !strings.Contains(req.Task, "Description: Build the thing") {
			t.Errorf("planning prompt missing mission description: %.400s", req.Task)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("lead planning was never dispatched")
	}

	// A [PLANNING] assignment row must exist for the lead.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE group_id = 'm-plan' AND assigned_to_id = 'agent-lead' AND task LIKE '[PLANNING]%'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 planning assignment, got %d", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit after cancel")
	}
}
