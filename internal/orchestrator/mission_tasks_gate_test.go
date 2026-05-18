package orchestrator

// Regression suite for the cross-crew dispatch gate and surrounding
// task-completion contracts. The gate at mission_tasks.go:385 (the
// "crew X is not connected to crew Y" guard inside scheduleTask)
// was proven broken in prod yesterday — these tests lock down the
// invariants so the regression cannot silently return.
//
// Conventions:
//   - Each test has a doc comment explaining WHY the contract matters
//     (not just WHAT it asserts).
//   - We never modify production code or pre-existing tests; this
//     file is purely additive.
//   - We use the package-local setupTestDB/seedTestData helpers
//     defined in mission_test.go.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// --- areCrewsConnected helper tests ---
// These are direct unit tests on the SQL guard helper. They matter
// because scheduleTask delegates the entire access-control decision
// to this single boolean — if it returns the wrong answer, the
// dispatch gate silently fails open.

// TestMissionTasks_AreCrewsConnected_NoRow_ReturnsFalse asserts the
// "empty table" baseline: with zero rows in crew_connections,
// areCrewsConnected MUST return (false, nil) — not an error, not
// true, not a panic. This is the case that bit us in prod yesterday
// (a freshly provisioned workspace with no connections rows).
func TestMissionTasks_AreCrewsConnected_NoRow_ReturnsFalse(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	connected, err := engine.areCrewsConnected(context.Background(), "crew-a", "crew-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if connected {
		t.Error("expected connected=false when no rows exist, got true")
	}
}

// TestMissionTasks_AreCrewsConnected_Bidirectional_BothDirectionsTrue
// asserts the most common shape: a bidirectional row is symmetrical.
// If admins create the relationship A↔B, both (A,B) and (B,A) checks
// must return true. A regression here would force every workspace
// admin to create TWO rows per relationship.
func TestMissionTasks_AreCrewsConnected_Bidirectional_BothDirectionsTrue(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		VALUES ('cc-bidi', 'ws-1', 'crew-a', 'crew-b', 'bidirectional', 'active', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	for _, pair := range [][2]string{{"crew-a", "crew-b"}, {"crew-b", "crew-a"}} {
		connected, err := engine.areCrewsConnected(context.Background(), pair[0], pair[1])
		if err != nil {
			t.Fatalf("(%s,%s) unexpected error: %v", pair[0], pair[1], err)
		}
		if !connected {
			t.Errorf("(%s,%s) expected connected=true for bidirectional row", pair[0], pair[1])
		}
	}
}

// TestMissionTasks_AreCrewsConnected_Unidirectional_MatchesOnlyForwardDirection
// asserts the directional asymmetry contract. The row was inserted
// as A→B unidirectional, so:
//   - (A, B) — checking "can mission in A dispatch to agent in B?" — TRUE
//   - (B, A) — checking "can mission in B dispatch to agent in A?" — FALSE
//
// If this regressed and both returned true, unidirectional would be
// indistinguishable from bidirectional and the entire access model
// collapses.
func TestMissionTasks_AreCrewsConnected_Unidirectional_MatchesOnlyForwardDirection(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// A → B unidirectional
	if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		VALUES ('cc-uni', 'ws-1', 'crew-a', 'crew-b', 'unidirectional', 'active', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	// Forward direction (A → B): mission in A wants to talk to agent in B → allowed.
	connected, err := engine.areCrewsConnected(context.Background(), "crew-a", "crew-b")
	if err != nil {
		t.Fatalf("(A,B) unexpected error: %v", err)
	}
	if !connected {
		t.Error("unidirectional A→B: expected (A,B) to return true (matching direction)")
	}

	// Reverse direction (B → A): mission in B wants to talk to agent in A → blocked.
	connected, err = engine.areCrewsConnected(context.Background(), "crew-b", "crew-a")
	if err != nil {
		t.Fatalf("(B,A) unexpected error: %v", err)
	}
	if connected {
		t.Error("unidirectional A→B: expected (B,A) to return false (wrong direction)")
	}
}

// --- scheduleTask gate tests (the high-level path that uses
// areCrewsConnected). These differ from the existing
// TestScheduleTask_CrossCrew_{Connected,NotConnected} by exercising
// the inactive-status branch and the same-crew bypass — neither of
// which is covered today. ---

// TestMissionTasks_CrossCrewDispatch_InactiveConnection_Blocked locks
// down that a status="inactive" row is treated as if no row exists.
// Production reality: admins disable a connection temporarily (e.g.
// during a partner offboarding) without deleting it. The gate MUST
// honor status, not row-existence — otherwise "soft-pausing" a
// relationship would have no effect and traffic would keep flowing.
func TestMissionTasks_CrossCrewDispatch_InactiveConnection_Blocked(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`CREATE TABLE crew_connections (
		id TEXT PRIMARY KEY, workspace_id TEXT, from_crew_id TEXT, to_crew_id TEXT,
		direction TEXT DEFAULT 'bidirectional', status TEXT DEFAULT 'active',
		created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	wsID := "ws-1"
	crewA := "crew-a"
	crewB := "crew-b"
	leadID := "agent-lead"
	crossAgentID := "agent-cross"

	_, _ = db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws')`, wsID)
	_, _ = db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew A', 'crew-a')`, crewA, wsID)
	_, _ = db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew B', 'crew-b')`, crewB, wsID)
	_, _ = db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Lead', 'lead', 'LEAD')`, leadID, wsID, crewA)
	_, _ = db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Cross', 'cross', 'AGENT')`, crossAgentID, wsID, crewB)

	now := time.Now().UTC().Format(time.RFC3339)
	// Row exists but status='inactive' — must be treated as no-connection.
	_, _ = db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		VALUES ('cc-inactive', ?, ?, ?, 'bidirectional', 'inactive', ?, ?)`, wsID, crewA, crewB, now, now)

	missionID := "mission-inactive"
	_, _ = db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-inactive', 'Mission', 'IN_PROGRESS', ?, ?)`,
		missionID, wsID, crewA, leadID, now, now)
	_, _ = db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-inactive', ?, ?, 'Cross task', 'PENDING', 1, '[]', ?, ?)`,
		missionID, crossAgentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	ms := &missionState{
		ID: missionID, CrewID: crewA, CrewSlug: "crew-a",
		LeadAgentID: leadID, TraceID: "trace-inactive", WorkspaceID: wsID,
	}
	task := TaskInfo{
		ID: "t-inactive", MissionID: missionID, AssignedAgentID: &crossAgentID,
		Title: "Cross task", Status: "PENDING",
	}

	err := engine.scheduleTask(context.Background(), ms, task, nil)
	if err == nil {
		t.Fatal("expected error when crew_connections.status='inactive', got nil")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("expected 'not connected' in error, got: %v", err)
	}

	// And the task must not have been transitioned to IN_PROGRESS.
	var stored string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-inactive'`).Scan(&stored); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if stored != "PENDING" {
		t.Errorf("expected task to remain PENDING after blocked dispatch, got %s", stored)
	}
}

// TestMissionTasks_SameCrewDispatch_NoConnectionRow_Succeeds asserts
// that dispatching to an agent in the SAME crew as the mission must
// NOT require any crew_connections row. A regression here (e.g.
// accidentally tightening the gate to always check connections)
// would break every single-crew mission — i.e. the default case.
func TestMissionTasks_SameCrewDispatch_NoConnectionRow_Succeeds(t *testing.T) {
	db := setupTestDB(t)
	// crew_connections table is intentionally NOT created — the same-crew
	// path must not query it at all.

	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-same', ?, ?, 'Same-crew task', 'PENDING', 1, '[]', ?, ?)`,
		missionID, agentID, now, now); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	disp := &mockDispatcher{}
	engine.SetDispatcher(disp)

	ms := &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		LeadAgentID: leadID, TraceID: "trace-same", WorkspaceID: wsID,
	}
	task := TaskInfo{
		ID: "t-same", MissionID: missionID, AssignedAgentID: &agentID,
		Title: "Same-crew task", Status: "PENDING",
	}

	if err := engine.scheduleTask(context.Background(), ms, task, nil); err != nil {
		t.Fatalf("same-crew dispatch should succeed without connection check, got: %v", err)
	}

	// Task transitioned to IN_PROGRESS.
	var status string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-same'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("expected status IN_PROGRESS, got %s", status)
	}

	// Wait for the dispatcher goroutine.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(disp.snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := disp.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(got))
	}
	if got[0].CrewID != crewID {
		t.Errorf("expected dispatch to crewID=%s, got %s", crewID, got[0].CrewID)
	}
}

// --- Task completion contract tests ---
// The existing TestOnAssignmentCompleted covers the happy COMPLETED
// path with result_summary. These add the FAILED+error_message
// branch and an explicit cross-tenant scope check — both of which
// have bitten us before (errors getting stored in result_summary
// instead of error_message, plus task lookups crossing missions).

// TestMissionTasks_OnAssignmentCompleted_FailedStatus_PersistsErrorMessage
// asserts that when an assignment finishes with status="FAILED",
// the orchestrator writes the error_message column (not
// result_summary) and the task's terminal status is FAILED.
// Surfacing failure causes is critical for user debugging — if the
// error column gets clobbered or written to the wrong column, the
// UI shows "task failed" with no explanation.
func TestMissionTasks_OnAssignmentCompleted_FailedStatus_PersistsErrorMessage(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES ('t-fail', ?, ?, 'Failing task', 'IN_PROGRESS', 1, '[]', 'assign-fail', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	engine.mu.Lock()
	engine.active[missionID] = &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		WorkspaceID: wsID, TraceID: "mission-trace-1",
		cancel: func() {},
	}
	engine.mu.Unlock()

	if err := engine.OnAssignmentCompleted(context.Background(), "assign-fail", "FAILED", "", "container OOM"); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}

	var status, errMsg, resultSummary string
	if err := db.QueryRow(`SELECT status, COALESCE(error_message,''), COALESCE(result_summary,'') FROM mission_tasks WHERE id = 't-fail'`).
		Scan(&status, &errMsg, &resultSummary); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("expected status FAILED, got %s", status)
	}
	if errMsg != "container OOM" {
		t.Errorf("expected error_message='container OOM', got %q", errMsg)
	}
	if resultSummary != "" {
		t.Errorf("expected empty result_summary on FAILED, got %q", resultSummary)
	}
}

// TestMissionTasks_OnAssignmentCompleted_TimeoutMapsToFailed asserts
// that a TIMEOUT status from the dispatcher is treated as a terminal
// FAILED outcome (per the switch in OnAssignmentCompleted). Without
// this contract, timeouts would leak through as COMPLETED-with-no-
// summary, hiding from the inbox.
func TestMissionTasks_OnAssignmentCompleted_TimeoutMapsToFailed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES ('t-timeout', ?, ?, 'Slow task', 'IN_PROGRESS', 1, '[]', 'assign-timeout', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	engine.mu.Lock()
	engine.active[missionID] = &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		WorkspaceID: wsID, TraceID: "mission-trace-1",
		cancel: func() {},
	}
	engine.mu.Unlock()

	if err := engine.OnAssignmentCompleted(context.Background(), "assign-timeout", "TIMEOUT", "", "30s wall clock exceeded"); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}

	var status, errMsg string
	if err := db.QueryRow(`SELECT status, COALESCE(error_message,'') FROM mission_tasks WHERE id = 't-timeout'`).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("TIMEOUT must map to FAILED, got %s", status)
	}
	if errMsg != "30s wall clock exceeded" {
		t.Errorf("expected error_message preserved, got %q", errMsg)
	}
}

// TestMissionTasks_OnAssignmentCompleted_CompletedPersistsResultSummary
// is a focused regression guard on the COMPLETED branch — it
// complements the existing TestOnAssignmentCompleted by also
// asserting that error_message stays empty (i.e. the two columns
// don't get cross-written).
func TestMissionTasks_OnAssignmentCompleted_CompletedPersistsResultSummary(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES ('t-ok', ?, ?, 'Happy task', 'IN_PROGRESS', 1, '[]', 'assign-ok', ?, ?)`,
		missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	engine := NewMissionEngine(db, nil, nil, logger)

	engine.mu.Lock()
	engine.active[missionID] = &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		WorkspaceID: wsID, TraceID: "mission-trace-1",
		cancel: func() {},
	}
	engine.mu.Unlock()

	if err := engine.OnAssignmentCompleted(context.Background(), "assign-ok", "COMPLETED", "Wrote 3 files, all tests green.", ""); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}

	var status, resultSummary, errMsg string
	if err := db.QueryRow(`SELECT status, COALESCE(result_summary,''), COALESCE(error_message,'') FROM mission_tasks WHERE id = 't-ok'`).
		Scan(&status, &resultSummary, &errMsg); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", status)
	}
	if !strings.Contains(resultSummary, "Wrote 3 files") {
		t.Errorf("expected result_summary to contain output, got %q", resultSummary)
	}
	if errMsg != "" {
		t.Errorf("expected empty error_message on COMPLETED, got %q", errMsg)
	}
}

// --- BuildLeadContext contract test ---
// The existing TestBuildLeadContext exercises shape and per-field
// inclusion. This adds a focused regression on the agent-list
// ORDERING and the slug-link contract: the lead's system prompt
// must list members in the order received, with each slug appearing
// in the same line as its name. If membership order regressed (e.g.
// from a map iteration), the lead would receive a non-deterministic
// roster and downstream prompt-caching would silently invalidate.
func TestLead_BuildLeadContext_PreservesMemberOrderAndSlugLinkage(t *testing.T) {
	members := []CrewMember{
		{Name: "Alpha", Slug: "alpha", RoleTitle: "Tester"},
		{Name: "Bravo", Slug: "bravo", RoleTitle: "Builder"},
		{Name: "Charlie", Slug: "charlie", RoleTitle: "Coder"},
	}
	out := BuildLeadContext(members)

	if out == "" {
		t.Fatal("expected non-empty lead context")
	}

	// All three slugs must appear, in input order.
	idxA := strings.Index(out, "@alpha")
	idxB := strings.Index(out, "@bravo")
	idxC := strings.Index(out, "@charlie")
	if idxA < 0 || idxB < 0 || idxC < 0 {
		t.Fatalf("missing slug: alpha=%d bravo=%d charlie=%d", idxA, idxB, idxC)
	}
	if !(idxA < idxB && idxB < idxC) {
		t.Errorf("expected member order alpha < bravo < charlie, got positions %d, %d, %d", idxA, idxB, idxC)
	}

	// And each name must precede its slug on the same line.
	for _, m := range members {
		needle := m.Name + " (@" + m.Slug
		if !strings.Contains(out, needle) {
			t.Errorf("expected %q to appear in context (name-slug linkage)", needle)
		}
	}

	// Block markers must wrap the whole thing — otherwise the lead
	// prompt loses its [CREW CONTEXT] sentinels and downstream parsers
	// can't locate the section.
	if !strings.Contains(out, "[CREW CONTEXT]") {
		t.Error("expected [CREW CONTEXT] opening marker")
	}
	if !strings.Contains(out, "[END CREW CONTEXT]") {
		t.Error("expected [END CREW CONTEXT] closing marker")
	}
}
