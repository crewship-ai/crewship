package orchestrator

// Atomicity tests for the mission-task write paths (issue #1892, task F).
//
// SQLite has exactly one writer database-wide, so every standalone statement
// is its own transaction AND its own acquisition of that lock. These tests
// pin two consequences:
//
//   - scheduleTask's "INSERT assignment" + "link it to the task" must be ONE
//     transaction. Split, a crash (or any failure) between them leaves an
//     assignment row nothing links back to while the task sits IN_PROGRESS —
//     resolveReadyFromTasks only re-picks PENDING/BLOCKED, so the work is
//     stranded with no operator-visible symptom.
//   - the cascade loops (unblockDependentTasks, failDependentTasksRecurse)
//     must apply as one batch, not row-by-row, so a mid-loop failure cannot
//     leave half a dependency frontier moved.
//
// The failure is injected with a BEFORE UPDATE trigger that RAISE(ABORT)s on
// the exact statement under test — the closest deterministic stand-in for
// "the process died right here".

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// missionAtomicityTask inserts one mission_tasks row in the given state.
func missionAtomicityTask(t *testing.T, db *sql.DB, id, missionID, agentID, status, deps string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		id, missionID, agentID, "Task "+id, status, deps, now, now)
}

// missionAtomicityAssignmentIDOf returns mission_tasks.assignment_id (empty
// when NULL).
func missionAtomicityAssignmentIDOf(t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var linked sql.NullString
	if err := db.QueryRow(`SELECT assignment_id FROM mission_tasks WHERE id = ?`, taskID).Scan(&linked); err != nil {
		t.Fatalf("read assignment_id of %s: %v", taskID, err)
	}
	return linked.String
}

func missionAtomicityCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// F1 — scheduleTask: assignment INSERT + task link are one transaction
// ---------------------------------------------------------------------------

func TestScheduleTaskAssignmentAndLinkAreAtomic(t *testing.T) {
	t.Parallel()

	// Fires only on the link statement: the IN_PROGRESS flip earlier in
	// scheduleTask does not name assignment_id, and the deferral unwind sets
	// it back to NULL, so neither trips this.
	const breakLink = `
		CREATE TRIGGER missionAtomicity_break_link
		BEFORE UPDATE OF assignment_id ON mission_tasks
		WHEN NEW.assignment_id IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'simulated crash between assignment insert and link'); END;`

	tests := []struct {
		name           string
		trigger        string
		wantErr        bool
		wantAssignRows int
		wantLinked     bool
	}{
		{
			name:           "both writes succeed",
			wantAssignRows: 1,
			wantLinked:     true,
		},
		{
			name:           "link fails: assignment row must not survive",
			trigger:        breakLink,
			wantErr:        true,
			wantAssignRows: 0,
			wantLinked:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := covMissionDB(t)
			covSeed(t, db)
			ms := covMission(t, db, "m1", "IN_PROGRESS")
			missionAtomicityTask(t, db, "t1", "m1", "agent-worker", "PENDING", "[]")
			if tc.trigger != "" {
				mustExec(t, db, tc.trigger)
			}

			e := newLifecycleEngine(t, db)
			worker := "agent-worker"
			err := e.scheduleTask(context.Background(), ms,
				TaskInfo{ID: "t1", AssignedAgentID: &worker, Title: "Task t1", DependsOn: "[]"}, nil)

			if tc.wantErr && err == nil {
				t.Fatalf("scheduleTask must fail when the link write fails, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("scheduleTask: %v", err)
			}

			got := missionAtomicityCount(t, db, `SELECT COUNT(*) FROM assignments WHERE group_id = 'm1'`)
			if got != tc.wantAssignRows {
				t.Errorf("assignments for m1 = %d, want %d — a row no task links back to is stranded work", got, tc.wantAssignRows)
			}
			linked := missionAtomicityAssignmentIDOf(t, db, "t1")
			if tc.wantLinked && linked == "" {
				t.Errorf("mission_tasks.assignment_id must be set on success")
			}
			if !tc.wantLinked && linked != "" {
				t.Errorf("mission_tasks.assignment_id = %q, want empty after rollback", linked)
			}
			if tc.wantLinked && got == 1 {
				var id string
				if err := db.QueryRow(`SELECT id FROM assignments WHERE group_id = 'm1'`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				if id != linked {
					t.Errorf("task links to %q but the assignment row is %q", linked, id)
				}
			}
		})
	}
}

// TestScheduleTaskFailedLinkLeavesNoStrandedTask is the operator-visible half
// of F1: when the assignment transaction cannot commit, the scheduler must
// surface it (the task ends FAILED) instead of leaving a task IN_PROGRESS
// that no tick will ever look at again.
func TestScheduleTaskFailedLinkLeavesNoStrandedTask(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	missionAtomicityTask(t, db, "t1", "m1", "agent-worker", "PENDING", "[]")
	mustExec(t, db, `
		CREATE TRIGGER missionAtomicity_break_link_sched
		BEFORE UPDATE OF assignment_id ON mission_tasks
		WHEN NEW.assignment_id IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'simulated crash'); END;`)

	e := newLifecycleEngine(t, db)
	if err := e.scheduleReadyTasks(context.Background(), ms); err != nil {
		t.Fatalf("scheduleReadyTasks: %v", err)
	}
	if got := covTaskStatus(t, db, "t1"); got != "FAILED" {
		t.Errorf("task status = %q, want FAILED — an IN_PROGRESS task with no assignment is stranded forever", got)
	}
	if n := missionAtomicityCount(t, db, `SELECT COUNT(*) FROM assignments WHERE group_id = 'm1'`); n != 0 {
		t.Errorf("orphan assignments = %d, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// F2 — batched cascade writes
// ---------------------------------------------------------------------------

func TestUnblockDependentTasksIsOneBatch(t *testing.T) {
	t.Parallel()

	// Aborts the unblock of exactly one row in the batch.
	const breakOne = `
		CREATE TRIGGER missionAtomicity_break_unblock
		BEFORE UPDATE OF status ON mission_tasks
		WHEN NEW.id = 't-b' AND NEW.status = 'PENDING'
		BEGIN SELECT RAISE(ABORT, 'simulated crash mid-batch'); END;`

	tests := []struct {
		name       string
		trigger    string
		wantStatus map[string]string
	}{
		{
			name: "every ready dependent is unblocked",
			wantStatus: map[string]string{
				"t-a": "PENDING", "t-b": "PENDING", "t-waiting": "BLOCKED",
			},
		},
		{
			name:    "a failure mid-batch unblocks nobody",
			trigger: breakOne,
			wantStatus: map[string]string{
				"t-a": "BLOCKED", "t-b": "BLOCKED", "t-waiting": "BLOCKED",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := covMissionDB(t)
			covSeed(t, db)
			covMission(t, db, "m1", "IN_PROGRESS")
			missionAtomicityTask(t, db, "t-done", "m1", "agent-worker", "COMPLETED", "[]")
			missionAtomicityTask(t, db, "t-other", "m1", "agent-worker", "PENDING", "[]")
			missionAtomicityTask(t, db, "t-a", "m1", "agent-worker", "BLOCKED", `["t-done"]`)
			missionAtomicityTask(t, db, "t-b", "m1", "agent-worker", "BLOCKED", `["t-done"]`)
			// Depends on t-done AND on a task that is still open, so it must
			// stay BLOCKED in every case.
			missionAtomicityTask(t, db, "t-waiting", "m1", "agent-worker", "BLOCKED", `["t-done","t-other"]`)
			if tc.trigger != "" {
				mustExec(t, db, tc.trigger)
			}

			e := newLifecycleEngine(t, db)
			e.unblockDependentTasks(context.Background(), "m1", "t-done")

			for id, want := range tc.wantStatus {
				if got := covTaskStatus(t, db, id); got != want {
					t.Errorf("task %s = %q, want %q", id, got, want)
				}
			}
		})
	}
}

func TestFailDependentTasksIsOneBatchPerLevel(t *testing.T) {
	t.Parallel()

	const breakOne = `
		CREATE TRIGGER missionAtomicity_break_cascade
		BEFORE UPDATE OF status ON mission_tasks
		WHEN NEW.id = 't-b' AND NEW.status = 'FAILED'
		BEGIN SELECT RAISE(ABORT, 'simulated crash mid-cascade'); END;`

	tests := []struct {
		name       string
		trigger    string
		wantStatus map[string]string
	}{
		{
			name: "cascade fails the whole dependent subtree",
			wantStatus: map[string]string{
				"t-a": "FAILED", "t-b": "FAILED", "t-deep": "FAILED", "t-unrelated": "BLOCKED",
			},
		},
		{
			name:    "a failure mid-batch fails nobody",
			trigger: breakOne,
			wantStatus: map[string]string{
				"t-a": "BLOCKED", "t-b": "BLOCKED", "t-deep": "BLOCKED", "t-unrelated": "BLOCKED",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := covMissionDB(t)
			covSeed(t, db)
			covMission(t, db, "m1", "IN_PROGRESS")
			missionAtomicityTask(t, db, "t-root", "m1", "agent-worker", "FAILED", "[]")
			missionAtomicityTask(t, db, "t-a", "m1", "agent-worker", "BLOCKED", `["t-root"]`)
			missionAtomicityTask(t, db, "t-b", "m1", "agent-worker", "BLOCKED", `["t-root"]`)
			missionAtomicityTask(t, db, "t-deep", "m1", "agent-worker", "BLOCKED", `["t-a"]`)
			missionAtomicityTask(t, db, "t-unrelated", "m1", "agent-worker", "BLOCKED", `["t-other"]`)
			if tc.trigger != "" {
				mustExec(t, db, tc.trigger)
			}

			e := newLifecycleEngine(t, db)
			e.failDependentTasks(context.Background(), "m1", "t-root", "upstream task failed")

			for id, want := range tc.wantStatus {
				if got := covTaskStatus(t, db, id); got != want {
					t.Errorf("task %s = %q, want %q", id, got, want)
				}
			}
		})
	}
}
