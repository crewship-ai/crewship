package orchestrator

// A hold is a WAIT, not a failure and not a retry loop.
//
// These tests drive the two mission callers that were broken by treating
// "the target agent is PENDING_REVIEW" as an ordinary dispatch error:
//
//   - scheduleReadyTasks classified it as "fail this task", so an ephemeral
//     hire waiting for the operator approval that guided autonomy REQUIRES
//     came back to a terminally FAILED mission task. Approving the hire
//     retried nothing.
//   - dispatchLeadPlanning classified it as "retry next tick", and because
//     the answer does not change until a human acts, it re-INSERTed a
//     planning assignment row and logged an ERROR every three seconds for
//     the life of the mission.
//
// The tests that matter most here are the LEGITIMATE-FLOW ones: a held hire
// must still complete its task once approved, and lead planning must still
// dispatch once the lead is approved. The refusal half is pinned alongside
// them so "defer" can never quietly become "run it anyway".

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// holdDispatcher records every dispatch it is handed and answers with a
// configurable error. Unlike covDispatcher it never blocks on an unread
// channel, so a test can assert "nothing was dispatched" without a timeout.
type holdDispatcher struct {
	mu   sync.Mutex
	got  []DispatchRequest
	err  error
	done chan struct{}
}

func newHoldDispatcher(err error) *holdDispatcher {
	return &holdDispatcher{err: err, done: make(chan struct{}, 16)}
}

func (d *holdDispatcher) DispatchAssignment(_ context.Context, r DispatchRequest) error {
	d.mu.Lock()
	d.got = append(d.got, r)
	err := d.err
	d.mu.Unlock()
	d.done <- struct{}{}
	return err
}

func (d *holdDispatcher) snapshot() []DispatchRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DispatchRequest, len(d.got))
	copy(out, d.got)
	return out
}

// waitForDispatch blocks until n dispatches have been observed, or fails.
func (d *holdDispatcher) waitForDispatch(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-d.done:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected %d dispatch(es); saw %d", n, len(d.snapshot()))
		}
	}
}

// settleUntil gives the dispatch goroutine (and any unwind it performs) a
// bounded window to finish before the test reads the DB. Used only where the
// assertion is about what that goroutine WROTE.
func settleUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not reached within 2s: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func holdCountAssignments(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	return n
}

func holdActivate(e *MissionEngine, ms *missionState) {
	e.mu.Lock()
	e.active[ms.ID] = ms
	e.mu.Unlock()
}

// ── 1. the legitimate flow: a guided ephemeral hire waits, then works ───────
//
// This is the case the previous round reasoned about and got wrong. A hire
// made under guided autonomy lands PENDING_REVIEW *by design* — the CLI polls
// exactly that transition (`crewship hire --wait`) — and the mission's task
// list already names it. Before this fix the first tick failed the task
// terminally, so the operator's approval arrived at a mission that had already
// given up.
//
// The assertion is on the WHOLE flow, not on the refusal: held → nothing
// written; approved → dispatched, IN_PROGRESS, one row; finished → COMPLETED.

func TestScheduleReadyTasks_HeldHireWaitsAndCompletesOnceApproved(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, workerID := covSeed(t, db)
	ms := covMission(t, db, "m-hire", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	disp := newHoldDispatcher(nil)
	e.SetDispatcher(disp)
	holdActivate(e, ms)

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-hire', ?, ?, 'Ship it', 'PENDING', 1, '[]', ?, ?)`, ms.ID, workerID, now, now)

	// The hire is staged for the operator.
	mustExec(t, db, `UPDATE agents SET status = 'PENDING_REVIEW' WHERE id = ?`, workerID)

	// Tick 1 — while the hold stands.
	if err := e.scheduleReadyTasks(context.Background(), ms); err != nil {
		t.Fatalf("scheduleReadyTasks (held): %v", err)
	}
	if got := covTaskStatus(t, db, "t-hire"); got != "PENDING" {
		t.Fatalf("task status while the hire awaits approval = %q, want PENDING — "+
			"a hold was recorded as a terminal outcome, so approving the hire retries nothing", got)
	}
	if n := holdCountAssignments(t, db); n != 0 {
		t.Errorf("assignment rows while held = %d, want 0", n)
	}
	if got := len(disp.snapshot()); got != 0 {
		t.Fatalf("dispatches while held = %d, want 0 — a PENDING_REVIEW agent was given work", got)
	}

	// The operator approves. This is exactly what ApproveHire does to the row
	// (see api's TestApproveHire_FlipsPendingReviewToIdle).
	mustExec(t, db, `UPDATE agents SET status = 'IDLE' WHERE id = ?`, workerID)

	// Tick 2 — the same task, nobody re-created it.
	if err := e.scheduleReadyTasks(context.Background(), ms); err != nil {
		t.Fatalf("scheduleReadyTasks (approved): %v", err)
	}
	disp.waitForDispatch(t, 1)
	if got := covTaskStatus(t, db, "t-hire"); got != "IN_PROGRESS" {
		t.Fatalf("task status after approval = %q, want IN_PROGRESS", got)
	}
	if n := holdCountAssignments(t, db); n != 1 {
		t.Fatalf("assignment rows after approval = %d, want exactly 1 "+
			"(the held tick must not have left a row behind)", n)
	}

	// And it finishes: the mission task reaches a real terminal state.
	var assignmentID string
	if err := db.QueryRow(`SELECT id FROM assignments`).Scan(&assignmentID); err != nil {
		t.Fatalf("read assignment id: %v", err)
	}
	if err := e.OnAssignmentCompleted(context.Background(), assignmentID, "COMPLETED", "shipped", ""); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}
	if got := covTaskStatus(t, db, "t-hire"); got != "COMPLETED" {
		t.Fatalf("task status after the run finished = %q, want COMPLETED", got)
	}
}

// ── 2. the half that must stay red if broken: a held agent never runs ───────
//
// The deferral must not become "skip the check". Ten ticks against a standing
// hold: no dispatch, no rows, and the task does not silently drop out of the
// mission by being marked terminal either.

func TestScheduleReadyTasks_HeldAgentIsNeverDispatchedWhileTheHoldStands(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, workerID := covSeed(t, db)
	ms := covMission(t, db, "m-held", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	disp := newHoldDispatcher(nil)
	e.SetDispatcher(disp)
	holdActivate(e, ms)

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-held', ?, ?, 'Do not run me', 'PENDING', 1, '[]', ?, ?)`, ms.ID, workerID, now, now)
	mustExec(t, db, `UPDATE agents SET status = 'PENDING_REVIEW' WHERE id = ?`, workerID)

	for i := 0; i < 10; i++ {
		if err := e.scheduleReadyTasks(context.Background(), ms); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := len(disp.snapshot()); got != 0 {
		t.Fatalf("dispatches across 10 ticks = %d, want 0 — the hold stopped being enforced", got)
	}
	if n := holdCountAssignments(t, db); n != 0 {
		t.Fatalf("assignment rows across 10 ticks = %d, want 0 — a held target still burns a row per tick", n)
	}
	if got := covTaskStatus(t, db, "t-held"); got != "PENDING" {
		t.Fatalf("task status = %q, want PENDING — waiting is not a terminal outcome", got)
	}
}

// ── 3. lead planning does not insert a second row on the next tick ──────────
//
// dispatchLeadPlanning INSERTed its assignment row and only then asked the
// dispatcher, which said no; the loop reset planningDispatched and did it all
// again on the next tick. Two ticks used to mean two PENDING rows nobody would
// ever run, and the mission never reached a terminal state.

func TestDispatchLeadPlanning_HeldLeadWritesNoRowPerTick(t *testing.T) {
	db := covMissionDB(t)
	_, _, leadID, _ := covSeed(t, db)
	ms := covMission(t, db, "m-plan", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	disp := newHoldDispatcher(nil)
	e.SetDispatcher(disp)

	mustExec(t, db, `UPDATE agents SET status = 'PENDING_REVIEW' WHERE id = ?`, leadID)

	for i := 1; i <= 2; i++ {
		err := e.dispatchLeadPlanning(context.Background(), ms)
		if err == nil {
			t.Fatalf("tick %d: dispatchLeadPlanning returned nil for a held lead", i)
		}
		if !isDeferredDispatch(err) {
			t.Fatalf("tick %d: error %v is not a deferral — the loop will treat it as a broken "+
				"dispatch and spin, or as a failure and stop", i, err)
		}
		if n := holdCountAssignments(t, db); n != 0 {
			t.Fatalf("tick %d: assignment rows = %d, want 0 — a standing hold is still writing "+
				"a planning row per tick", i, n)
		}
	}
	if got := len(disp.snapshot()); got != 0 {
		t.Fatalf("dispatches = %d, want 0", got)
	}

	// MUTATION half: approve the lead and the identical call plans, once.
	mustExec(t, db, `UPDATE agents SET status = 'IDLE' WHERE id = ?`, leadID)
	if err := e.dispatchLeadPlanning(context.Background(), ms); err != nil {
		t.Fatalf("dispatchLeadPlanning after approval: %v — the guard refuses unconditionally", err)
	}
	disp.waitForDispatch(t, 1)
	if n := holdCountAssignments(t, db); n != 1 {
		t.Fatalf("assignment rows after approval = %d, want 1", n)
	}
	if got := disp.snapshot()[0].AgentID; got != leadID {
		t.Errorf("planning dispatched to %q, want the lead %q", got, leadID)
	}
}

// ── 4. the door's deferral unwinds the row the caller already wrote ─────────
//
// The admission read and the INSERT are not one statement, so an agent staged
// in between reaches DispatchAssignment, which refuses. That refusal arrives
// at the goroutine AFTER the row exists and after the task is IN_PROGRESS. It
// must unwind both — otherwise the task is stuck IN_PROGRESS forever and the
// orphan PENDING row sits in the ledger.
//
// The deferral is constructed by a type declared HERE, implementing only
// DispatchDeferred(), to prove the classification is structural: the real one
// on this path is api's *agentHeldError, which this package cannot import.

type doorDeferral struct{}

func (doorDeferral) Error() string     { return "agent x is PENDING_REVIEW" }
func (doorDeferral) DispatchDeferred() {}

func TestScheduleTask_DoorDeferralUnwindsTheRowAndLeavesTheTaskPending(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, workerID := covSeed(t, db)
	ms := covMission(t, db, "m-race", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	// Wrapped, so a classification that only type-asserts the top-level error
	// fails here the way errors.As would not.
	disp := newHoldDispatcher(fmt.Errorf("dispatch: %w", doorDeferral{}))
	e.SetDispatcher(disp)
	holdActivate(e, ms)

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-race', ?, ?, 'Raced', 'PENDING', 1, '[]', ?, ?)`, ms.ID, workerID, now, now)

	task := TaskInfo{ID: "t-race", MissionID: ms.ID, AssignedAgentID: &workerID, Title: "Raced", Status: "PENDING"}
	if err := e.scheduleTask(context.Background(), ms, task, nil); err != nil {
		t.Fatalf("scheduleTask: %v", err)
	}
	disp.waitForDispatch(t, 1)
	settleUntil(t, func() bool { return covTaskStatus(t, db, "t-race") != "IN_PROGRESS" },
		"the task left IN_PROGRESS after the door deferred")

	if got := covTaskStatus(t, db, "t-race"); got != "PENDING" {
		t.Fatalf("task status after a deferral from the door = %q, want PENDING — "+
			"a wait was recorded as a terminal failure", got)
	}
	// COALESCE + MAX rather than a bare scan: an unwind that DELETES the row
	// is an acceptable outcome too, and a plain QueryRow would then fail with
	// "sql: no rows in result set" — a message that reads like the test could
	// not run, on a run where the code did the right thing. What must not
	// happen is a row left PENDING, so ask exactly that.
	var status string
	if err := db.QueryRow(`SELECT COALESCE(MAX(status),'<deleted>') FROM assignments`).Scan(&status); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if status == "PENDING" {
		t.Errorf("the unwound assignment is still PENDING — it will sit in the ledger forever")
	}
	var linked string
	if err := db.QueryRow(`SELECT COALESCE(assignment_id,'') FROM mission_tasks WHERE id = 't-race'`).Scan(&linked); err != nil {
		t.Fatalf("read task link: %v", err)
	}
	if linked != "" {
		t.Errorf("task still points at the unwound assignment %q", linked)
	}
}

// ── 5. an ORDINARY dispatch failure is still terminal ───────────────────────
//
// The deferral must be narrow. A dispatch that genuinely broke still fails the
// task loudly — otherwise every broken dispatch becomes an invisible retry.

func TestScheduleTask_OrdinaryDispatchFailureStillFailsTheTask(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, workerID := covSeed(t, db)
	ms := covMission(t, db, "m-boom", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	disp := newHoldDispatcher(errors.New("container refused to start"))
	e.SetDispatcher(disp)
	holdActivate(e, ms)

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-boom', ?, ?, 'Boom', 'PENDING', 1, '[]', ?, ?)`, ms.ID, workerID, now, now)

	task := TaskInfo{ID: "t-boom", MissionID: ms.ID, AssignedAgentID: &workerID, Title: "Boom", Status: "PENDING"}
	if err := e.scheduleTask(context.Background(), ms, task, nil); err != nil {
		t.Fatalf("scheduleTask: %v", err)
	}
	disp.waitForDispatch(t, 1)
	settleUntil(t, func() bool { return covTaskStatus(t, db, "t-boom") == "FAILED" },
		"an ordinary dispatch failure marked the task FAILED")
}

// ── 6. the discriminator: mission rows carry depth 0 ────────────────────────
//
// delegation_limits.go tells the mission engine's rows apart from the ones its
// capped doors write by `depth > 0`. That only works while the mission engine
// keeps writing 0, so pin it here: a future edit that stamps a depth on a
// mission row would silently make busy leads unmentionable again.

func TestMissionAssignmentRowsCarryDepthZero(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, workerID := covSeed(t, db)
	ms := covMission(t, db, "m-depth", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	disp := newHoldDispatcher(nil)
	e.SetDispatcher(disp)
	holdActivate(e, ms)

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-depth', ?, ?, 'Depth', 'PENDING', 1, '[]', ?, ?)`, ms.ID, workerID, now, now)

	task := TaskInfo{ID: "t-depth", MissionID: ms.ID, AssignedAgentID: &workerID, Title: "Depth", Status: "PENDING"}
	if err := e.scheduleTask(context.Background(), ms, task, nil); err != nil {
		t.Fatalf("scheduleTask: %v", err)
	}
	if err := e.dispatchLeadPlanning(context.Background(), ms); err != nil {
		t.Fatalf("dispatchLeadPlanning: %v", err)
	}
	disp.waitForDispatch(t, 2)

	rows, err := db.Query(`SELECT id, depth FROM assignments`)
	if err != nil {
		t.Fatalf("query assignments: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		var depth int
		if err := rows.Scan(&id, &depth); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if depth != 0 {
			t.Errorf("mission assignment %s has depth %d, want 0 — delegation_limits.go tells "+
				"mission rows from capped-door rows by exactly this column", id, depth)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate assignments: %v", err)
	}
	if n != 2 {
		t.Fatalf("assignment rows = %d, want 2 (one task + one planning)", n)
	}
}
