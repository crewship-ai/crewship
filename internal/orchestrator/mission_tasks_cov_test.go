package orchestrator

// Coverage tests for mission_tasks.go: ResolveReadyTasks self-heal and
// auto-assign branches, autoAssignTask fallbacks, buildMissionBrief
// content, scheduleTask guard rails (circuit breaker, deleted agent,
// cross-crew connection), and scheduleReadyTasks failure marking.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResolveReadyTasks_SelfHealsBlockedWithCompletedDeps(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Done', 'COMPLETED', 1, '[]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', 'm1', 'agent-worker', 'Stuck', 'BLOCKED', 2, '["t1"]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	ready, err := e.ResolveReadyTasks(context.Background(), "m1")
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "t2" {
		t.Fatalf("self-healed BLOCKED task must be ready, got %+v", ready)
	}
	if got := covTaskStatus(t, db, "t2"); got != "PENDING" {
		t.Errorf("self-heal must persist PENDING, got %q", got)
	}
}

func TestResolveReadyTasks_InvalidDependsOnSkipped(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-bad', 'm1', 'agent-worker', 'Bad deps', 'PENDING', 1, 'not-json', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	ready, err := e.ResolveReadyTasks(context.Background(), "m1")
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("task with malformed depends_on must not be ready, got %+v", ready)
	}
}

func TestResolveReadyTasks_AutoAssignsUnassignedTask(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-un', 'm1', 'Unowned', 'PENDING', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	ready, err := e.ResolveReadyTasks(context.Background(), "m1")
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("want 1 ready task, got %d", len(ready))
	}
	// Non-lead worker bob must win over the lead.
	if ready[0].AssignedAgentID == nil || *ready[0].AssignedAgentID != "agent-worker" {
		t.Errorf("auto-assign must pick the non-lead worker, got %+v", ready[0].AssignedAgentID)
	}
	var persisted string
	db.QueryRow(`SELECT assigned_agent_id FROM mission_tasks WHERE id = 't-un'`).Scan(&persisted)
	if persisted != "agent-worker" {
		t.Errorf("auto-assignment must be persisted, got %q", persisted)
	}
}

func TestResolveReadyTasks_AutoAssignFailureMarksTaskFailed(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	// Workspace and crew exist but the crew has NO agents and the mission's
	// lead_agent_id points nowhere → autoAssignTask must error.
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-1', 'WS', 'ws')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', 'ws-1', 'Crew', 'dev-crew')`)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-orphan', 'm1', 'Orphan', 'PENDING', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()

	ready, err := e.ResolveReadyTasks(context.Background(), "m1")
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("unassignable task must not be ready, got %+v", ready)
	}
	if got := covTaskStatus(t, db, "t-orphan"); got != "FAILED" {
		t.Errorf("unassignable task = %q, want FAILED", got)
	}
	var errMsg string
	db.QueryRow(`SELECT error_message FROM mission_tasks WHERE id = 't-orphan'`).Scan(&errMsg)
	if !strings.Contains(errMsg, "auto-assignment failed") {
		t.Errorf("error_message = %q, want auto-assignment failure reason", errMsg)
	}
}

func TestAutoAssignTask_LeadFallbackWhenNoWorkers(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-1', 'WS', 'ws')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', 'ws-1', 'Crew', 'dev-crew')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES ('agent-lead', 'ws-1', 'crew-1', 'Anna', 'anna', 'LEAD')`)
	covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'Solo', 'PENDING', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	id, slug, err := e.autoAssignTask(context.Background(), "m1", "t1")
	if err != nil {
		t.Fatalf("autoAssignTask: %v", err)
	}
	if id != "agent-lead" || slug != "anna" {
		t.Errorf("lead fallback expected, got id=%q slug=%q", id, slug)
	}
}

func TestAutoAssignTask_MissionNotFound(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	e := newLifecycleEngine(t, db)
	_, _, err := e.autoAssignTask(context.Background(), "nope", "t1")
	if err == nil || !strings.Contains(err.Error(), "lookup mission") {
		t.Fatalf("expected lookup mission error, got %v", err)
	}
}

func TestBuildMissionBrief_HandoffDepsCommentsAndRetry(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	mustExec(t, db, `UPDATE missions SET description = 'Ship the feature' WHERE id = 'm1'`)
	mustExec(t, db, `INSERT INTO users (id, name, email) VALUES ('u1', 'Pavel', 'p@x.cz')`)
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES ('c1', 'm1', 'agent', 'agent-worker', 'I found the root cause', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES ('c2', 'm1', 'user', 'u1', 'Please also update docs', ?, ?)`, now, now)

	handoffOut := "noise\n---HANDOFF---\nsummary: implemented the parser\nconfidence: high\nartifacts: parser.go\n---END HANDOFF---"
	dep := "t-dep"
	workerSlug := "bob"
	allTasks := []TaskInfo{
		{ID: dep, MissionID: "m1", Title: "Parse", Status: "COMPLETED", TaskOrder: 1,
			AgentSlug: &workerSlug, ResultSummary: &handoffOut},
		{ID: "t-cur", MissionID: "m1", Title: "Integrate", Status: "IN_PROGRESS", TaskOrder: 2,
			AgentSlug: &workerSlug, DependsOn: `["t-dep"]`},
	}
	desc := "Wire the parser into the pipeline"
	task := TaskInfo{
		ID: "t-cur", MissionID: "m1", Title: "Integrate", Description: &desc,
		DependsOn: `["t-dep"]`, Iteration: 2, TaskOrder: 2, AgentSlug: &workerSlug,
	}

	e := newLifecycleEngine(t, db)
	brief := e.buildMissionBrief(context.Background(), ms, task, allTasks)

	for _, want := range []string{
		"IMPORTANT: You are part of a multi-agent mission pipeline",
		"[MISSION]",
		"Goal: Ship the feature",
		"[INPUT FROM PREVIOUS TASKS]",
		"implemented the parser",
		"Artifacts: parser.go",
		"Confidence: high",
		"[ISSUE COMMENTS]",
		"@Bob: I found the root cause",
		"@Pavel: Please also update docs",
		"[YOUR ASSIGNMENT]",
		"Instructions: Wire the parser into the pipeline",
		"Iteration: 2",
		"---HANDOFF---",
		"Execute this task NOW",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing %q:\n%.1500s", want, brief)
		}
	}
}

func TestBuildMissionBrief_TruncatesLongDepOutputAndTotal(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")

	longOut := strings.Repeat("x", maxDepOutputLen+500) // no handoff block → raw truncation
	dep := "t-dep"
	allTasks := []TaskInfo{
		{ID: dep, Title: "Big", Status: "COMPLETED", TaskOrder: 1, ResultSummary: &longOut},
	}
	task := TaskInfo{ID: "t-cur", Title: "Next", DependsOn: `["t-dep"]`, Iteration: 1, TaskOrder: 2}

	e := newLifecycleEngine(t, db)
	brief := e.buildMissionBrief(context.Background(), ms, task, allTasks)
	if !strings.Contains(brief, "...(truncated)") {
		t.Errorf("oversized dependency output must be truncated")
	}
	if len(brief) > maxBriefTotalLen+100 {
		t.Errorf("brief length %d exceeds total cap", len(brief))
	}

	// Total cap: blow past 32KB via a giant description.
	huge := strings.Repeat("y", maxBriefTotalLen+1000)
	task2 := TaskInfo{ID: "t2", Title: "Huge", Description: &huge, Iteration: 1}
	brief2 := e.buildMissionBrief(context.Background(), ms, task2, nil)
	if !strings.Contains(brief2, "...(brief truncated to 32KB)") {
		t.Errorf("oversized brief must carry the 32KB truncation marker")
	}
}

func TestScheduleTask_CircuitBreakerTrips(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	e.cbMu.Lock()
	e.failures["agent-worker"] = circuitBreakerThreshold
	e.cbMu.Unlock()

	agentID := "agent-worker"
	err := e.scheduleTask(context.Background(), ms, TaskInfo{ID: "t1", AssignedAgentID: &agentID}, nil)
	if err == nil || !strings.Contains(err.Error(), "circuit breaker") {
		t.Fatalf("expected circuit breaker error, got %v", err)
	}
}

func TestScheduleTask_DeletedAgent(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	ghost := "agent-ghost"
	err := e.scheduleTask(context.Background(), ms, TaskInfo{ID: "t1", AssignedAgentID: &ghost}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected agent-not-found error, got %v", err)
	}
}

func TestScheduleTask_CrossCrewRequiresConnection(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	// Second crew with one agent, NOT connected to crew-1.
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-2', 'ws-1', 'Ops', 'ops-crew')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-remote', 'ws-1', 'crew-2', 'Rita', 'rita')`)
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-x', 'm1', 'agent-remote', 'Cross', 'PENDING', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	remote := "agent-remote"
	err := e.scheduleTask(context.Background(), ms, TaskInfo{ID: "t-x", AssignedAgentID: &remote, Title: "Cross"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected crew-connection error, got %v", err)
	}
}

func TestScheduleTask_CrossCrewConnectedDispatchFailureFailsTask(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-2', 'ws-1', 'Ops', 'ops-crew')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-remote', 'ws-1', 'crew-2', 'Rita', 'rita')`)
	// Bidirectional connection declared in the reverse direction.
	mustExec(t, db, `INSERT INTO crew_connections (id, from_crew_id, to_crew_id, status, direction)
		VALUES ('cc1', 'crew-2', 'crew-1', 'active', 'bidirectional')`)
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-x', 'm1', 'agent-remote', 'Cross', 'PENDING', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()
	d := newCovDispatcher(errors.New("container provisioning failed"))
	e.SetDispatcher(d)

	remote := "agent-remote"
	if err := e.scheduleTask(context.Background(), ms,
		TaskInfo{ID: "t-x", AssignedAgentID: &remote, Title: "Cross", DependsOn: "[]"}, nil); err != nil {
		t.Fatalf("scheduleTask: %v", err)
	}

	// Dispatch happens in a goroutine; verify the request shape first.
	select {
	case req := <-d.ch:
		if req.CrewID != "crew-2" || req.CrewSlug != "ops-crew" || req.AgentSlug != "rita" {
			t.Errorf("cross-crew dispatch must target the agent's crew: %+v", req)
		}
		if req.ChatID != "m1" || req.MissionID != "m1" {
			t.Errorf("dispatch must group by mission: %+v", req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher never invoked")
	}

	// The failing dispatch must mark the task FAILED (async — poll briefly).
	deadline := time.Now().Add(3 * time.Second)
	for {
		if covTaskStatus(t, db, "t-x") == "FAILED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task status = %q, want FAILED after dispatch error", covTaskStatus(t, db, "t-x"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	var errMsg string
	db.QueryRow(`SELECT error_message FROM mission_tasks WHERE id = 't-x'`).Scan(&errMsg)
	if !strings.Contains(errMsg, "container provisioning failed") {
		t.Errorf("dispatch error must be persisted, got %q", errMsg)
	}
}

func TestScheduleTask_AlreadyClaimedIsSilentNoop(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-claimed', 'm1', 'agent-worker', 'Busy', 'IN_PROGRESS', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	d := newCovDispatcher(nil)
	e.SetDispatcher(d)
	worker := "agent-worker"
	if err := e.scheduleTask(context.Background(), ms,
		TaskInfo{ID: "t-claimed", AssignedAgentID: &worker, Title: "Busy"}, nil); err != nil {
		t.Fatalf("already-claimed task must be a silent no-op, got %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE group_id = 'm1'`).Scan(&count)
	if count != 0 {
		t.Errorf("no assignment must be created for a claimed task, got %d", count)
	}
}

func TestScheduleReadyTasks_MarksFailedOnScheduleError(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	// Assigned agent ID points at a non-existent agent → scheduleTask errors.
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-bad', 'm1', 'agent-deleted', 'Doomed', 'PENDING', 1, '[]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	if err := e.scheduleReadyTasks(context.Background(), ms); err != nil {
		t.Fatalf("scheduleReadyTasks: %v", err)
	}
	if got := covTaskStatus(t, db, "t-bad"); got != "FAILED" {
		t.Errorf("unschedulable task = %q, want FAILED", got)
	}
}

func TestAreCrewsConnected_DirectAndNone(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-2', 'ws-1', 'Ops', 'ops')`)
	mustExec(t, db, `INSERT INTO crew_connections (id, from_crew_id, to_crew_id, status, direction)
		VALUES ('cc1', 'crew-1', 'crew-2', 'active', 'one-way')`)

	e := newLifecycleEngine(t, db)
	ok, err := e.areCrewsConnected(context.Background(), "crew-1", "crew-2")
	if err != nil || !ok {
		t.Errorf("direct connection must report connected, got ok=%v err=%v", ok, err)
	}
	// Reverse of a one-way connection: not connected.
	ok, err = e.areCrewsConnected(context.Background(), "crew-2", "crew-1")
	if err != nil || ok {
		t.Errorf("reverse one-way must NOT be connected, got ok=%v err=%v", ok, err)
	}
}
