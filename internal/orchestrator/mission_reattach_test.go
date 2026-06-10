package orchestrator

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// W5 — mission engine boot re-attach. runMissionLoop is in-memory only, so a
// server restart leaves IN_PROGRESS missions with no driver. The boot-time
// scan (ReattachInProgressMissions) must find them and re-attach loops via
// StartMission, which in turn must be idempotent so the scan can never
// double-start a mission that an API handler already revived.
// ---------------------------------------------------------------------------

// TestReattachInProgressMissions_TasksProgress is the W5 acceptance test:
// a mission sits IN_PROGRESS in the DB (as left behind by a crashed server),
// a fresh engine boots, and after the re-attach scan the orchestration loop
// is live again — observable through the task actually being dispatched.
func TestReattachInProgressMissions_TasksProgress(t *testing.T) {
	db := setupTestDB(t)
	// The re-attached loop runs in its own goroutine; with the default
	// pool, a concurrent query can land on a second connection — which,
	// for sqlite ":memory:", is a fresh empty database. Pin to one conn.
	db.SetMaxOpenConns(1)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID) // status IN_PROGRESS

	// One ready task, assigned — the previous process died before
	// dispatching it.
	insertTask(t, db, "t1", missionID, &agentID, "Resume me", "PENDING", 1, nil)

	// Fresh engine simulating a server restart: nothing in the active map.
	engine := newTestEngine(t, db)
	dispatcher := newMockAsyncDispatcher()
	engine.SetDispatcher(dispatcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer engine.Shutdown()

	n := engine.ReattachInProgressMissions(ctx)
	if n != 1 {
		t.Fatalf("ReattachInProgressMissions = %d, want 1", n)
	}

	// Loop is registered in the active map again.
	engine.mu.Lock()
	_, active := engine.active[missionID]
	engine.mu.Unlock()
	if !active {
		t.Fatal("mission not in active map after re-attach")
	}

	// And it actually drives work: the pending task gets dispatched on the
	// loop's tick (3s ticker — allow a generous margin).
	req := waitForDispatch(t, dispatcher.ch, 15*time.Second)
	if req.MissionID != missionID {
		t.Errorf("dispatched MissionID = %q, want %q", req.MissionID, missionID)
	}
	if req.AgentID != agentID {
		t.Errorf("dispatched AgentID = %q, want %q", req.AgentID, agentID)
	}

	// Task moved off PENDING — progress is persisted, not just in-memory.
	var status string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't1'`).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("task status = %q, want IN_PROGRESS", status)
	}
}

// A reattached loop must still run the BLOCKED→PENDING self-heal in
// ResolveReadyTasks: a task left BLOCKED whose dependency completed before
// the crash gets promoted and dispatched after the re-attach.
func TestReattachInProgressMissions_SelfHealsBlockedTask(t *testing.T) {
	db := setupTestDB(t)
	db.SetMaxOpenConns(1) // see TestReattachInProgressMissions_TasksProgress
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	insertTask(t, db, "t1", missionID, &agentID, "Done before crash", "COMPLETED", 1, nil)
	insertTask(t, db, "t2", missionID, &agentID, "Stranded blocked", "BLOCKED", 2, []string{"t1"})

	engine := newTestEngine(t, db)
	dispatcher := newMockAsyncDispatcher()
	engine.SetDispatcher(dispatcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer engine.Shutdown()

	if n := engine.ReattachInProgressMissions(ctx); n != 1 {
		t.Fatalf("ReattachInProgressMissions = %d, want 1", n)
	}

	req := waitForDispatch(t, dispatcher.ch, 15*time.Second)
	if req.MissionID != missionID {
		t.Errorf("dispatched MissionID = %q, want %q", req.MissionID, missionID)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't2'`).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("blocked task status after self-heal = %q, want IN_PROGRESS", status)
	}
}

// Only IN_PROGRESS missions get a driver — PLANNING waits for an explicit
// start, terminal states stay terminal.
func TestReattachInProgressMissions_SkipsNonInProgress(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, _ := seedTestData(t, db)

	now := time.Now().UTC().Format(time.RFC3339)
	for i, status := range []string{"PLANNING", "COMPLETED", "FAILED", "PAUSED"} {
		if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"m-"+status, wsID, crewID, leadID, "trace-"+status, "Mission "+status, status, now, now); err != nil {
			t.Fatalf("insert mission %d: %v", i, err)
		}
	}

	engine := newTestEngine(t, db)
	defer engine.Shutdown()

	if n := engine.ReattachInProgressMissions(context.Background()); n != 0 {
		t.Errorf("ReattachInProgressMissions = %d, want 0 (no IN_PROGRESS missions)", n)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.active) != 0 {
		t.Errorf("active map = %v, want empty", engine.active)
	}
}

// The scan must not double-attach a mission that already has a live loop
// (e.g. an API handler raced the boot scan). The existing state stays.
func TestReattachInProgressMissions_SkipsAlreadyActive(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, _ := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	engine := newTestEngine(t, db)
	defer engine.Shutdown()

	existing := &missionState{ID: missionID, cancel: func() {}}
	engine.mu.Lock()
	engine.active[missionID] = existing
	engine.mu.Unlock()

	if n := engine.ReattachInProgressMissions(context.Background()); n != 0 {
		t.Errorf("ReattachInProgressMissions = %d, want 0 (mission already active)", n)
	}
	engine.mu.Lock()
	got := engine.active[missionID]
	engine.mu.Unlock()
	if got != existing {
		t.Error("re-attach replaced the existing active mission state")
	}
}

// After Shutdown the scan must refuse to attach anything — the engine is
// going away and a fresh loop would leak.
func TestReattachInProgressMissions_NoopAfterShutdown(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, _ := seedTestData(t, db)
	createTestMission(t, db, wsID, crewID, leadID)

	engine := newTestEngine(t, db)
	engine.Shutdown()

	if n := engine.ReattachInProgressMissions(context.Background()); n != 0 {
		t.Errorf("ReattachInProgressMissions after Shutdown = %d, want 0", n)
	}
}
