package orchestrator

// Closes the last third of golden scenario 5a (PRD-ISSUES-AND-ROUTINES-2026
// §18): "STOP during a run → no further step is started, run ends
// CANCELLED, a late callback changes nothing."
//
// The other two thirds are proven elsewhere on this branch:
//   - issue_stop_cancel_test.go's TestRunAssignment_CancelRequested_StartsNoFurtherStep
//     proves a PENDING, pre-cancelled assignment never starts.
//   - mission_tasks_cancel_guard_test.go's
//     TestOnAssignmentCompleted_DoesNotResurrectCancelledTask proves a late
//     COMPLETED report does not flip a CANCELLED task back.
//
// Neither touches the case the promise is actually written for: a task
// mid-flight (RUNNING) when Stop lands, with a DOWNSTREAM task queued behind
// it. Tier 1 cancellation has no kill primitive (internal/provider/container.go
// has Exec/ExecInspect, no Kill — see assignments_run.go's finishAssignment
// comment), so task A's row legitimately stays IN_PROGRESS/RUNNING for a
// while. "The next step does not happen" is the only thing an observer can
// see that proves Stop did anything at all for a live run.

import (
	"context"
	"testing"
	"time"
)

// The writes below reproduce, verbatim, what IssueHandler.Stop does in one
// transaction (internal/api/issue_handler_workflow.go): mission_tasks →
// CANCELLED for every non-terminal row, assignments → cancel_requested_at
// stamped on every live (PENDING/RUNNING) row, missions → CANCELLED. The
// orchestrator package cannot import the api package (api imports
// orchestrator, so the reverse would be a cycle), so the equivalent SQL is
// inlined here, the same way mission_tasks_cancel_guard_test.go already
// simulates Stop's mission_tasks half.

func TestScheduleReadyTasks_StopMidRun_DownstreamTaskNeverDispatched(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, workerID := covSeed(t, db)
	ms := covMission(t, db, "m-stop-midflight", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	disp := newHoldDispatcher(nil)
	e.SetDispatcher(disp)
	holdActivate(e, ms)

	now := time.Now().UTC().Format(time.RFC3339)

	// Task A: mid-flight when Stop lands — IN_PROGRESS, with a live RUNNING
	// assignment already dispatched for it (exactly what scheduleTask leaves
	// behind once dispatch succeeds).
	mustExec(t, db, `INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES ('t-A', ?, ?, 'Task A', 'IN_PROGRESS', 1, '[]', 'a-running', ?, ?)`,
		ms.ID, workerID, now, now)
	mustExec(t, db, `INSERT INTO assignments
		(id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, mission_id, created_at, started_at)
		VALUES ('a-running', 'ws-1', ?, 'agent-lead', ?, 'do work', 'RUNNING', ?, ?, ?, ?)`,
		ms.ID, workerID, ms.ID, ms.ID, now, now)

	// Task B: queued behind A, never got as far as an assignment row.
	mustExec(t, db, `INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-B', ?, ?, 'Task B', 'PENDING', 2, '["t-A"]', ?, ?)`,
		ms.ID, workerID, now, now)

	// --- Stop lands (the exact writes IssueHandler.Stop makes) ---
	mustExec(t, db, `UPDATE mission_tasks SET status = 'CANCELLED', updated_at = ?
		WHERE mission_id = ? AND status IN ('PENDING', 'IN_PROGRESS', 'BLOCKED')`, now, ms.ID)
	mustExec(t, db, `UPDATE assignments SET cancel_requested_at = ?, cancel_reason = 'issue stopped'
		WHERE (mission_id = ? OR chat_id = ? OR group_id = ?) AND status IN ('PENDING', 'RUNNING')`,
		now, ms.ID, ms.ID, ms.ID)
	mustExec(t, db, `UPDATE missions SET status = 'CANCELLED', completed_at = ?, updated_at = ? WHERE id = ?`,
		now, now, ms.ID)

	// Stop's own blanket write already terminalizes the downstream task —
	// this is the primary mechanism, not merely "A never completes so B is
	// never ready". Assert it landed before touching the scheduler at all,
	// so a later failure below can't be misread as this write missing.
	if got := covTaskStatus(t, db, "t-B"); got != "CANCELLED" {
		t.Fatalf("t-B status right after Stop = %q, want CANCELLED — Stop's sweep must reach every "+
			"non-terminal task in the mission, not only the one with a live assignment", got)
	}

	// --- Drive the mission engine's next scheduling tick directly ---
	// (bypassing runMissionLoop's own outer "mission no longer IN_PROGRESS"
	// gate, which would already refuse to call this at all — this proves
	// the scheduler's OWN ready-resolution logic refuses B too, as a second,
	// independent guard).
	if err := e.scheduleReadyTasks(context.Background(), ms); err != nil {
		t.Fatalf("scheduleReadyTasks: %v", err)
	}

	if got := len(disp.snapshot()); got != 0 {
		t.Fatalf("dispatches after a mid-flight Stop = %d, want 0 — task B was started anyway", got)
	}
	if n := holdCountAssignments(t, db); n != 1 {
		t.Fatalf("assignment rows = %d, want 1 (only a-running, from before Stop) — "+
			"a new row means B was dispatched", n)
	}
	if got := covTaskStatus(t, db, "t-B"); got != "CANCELLED" {
		t.Fatalf("t-B status after the tick = %q, want still CANCELLED — the scheduler must not "+
			"resurrect it out of a terminal state", got)
	}
	if got := covTaskStatus(t, db, "t-A"); got != "CANCELLED" {
		t.Fatalf("t-A status after the tick = %q, want still CANCELLED", got)
	}

	// Belt and braces: even setting aside Stop's own sweep, B's dependency A
	// never reaches COMPLETED (it is CANCELLED), so resolveReadyFromTasks'
	// dependency check alone would keep B out of the ready set. Prove that
	// independently by re-running ready-resolution and checking B never
	// appears, rather than trusting the dispatch count alone.
	ready, err := e.ResolveReadyTasks(context.Background(), ms.ID)
	if err != nil {
		t.Fatalf("ResolveReadyTasks: %v", err)
	}
	for _, r := range ready {
		if r.ID == "t-B" {
			t.Fatalf("t-B appeared in the ready set — a cancelled dependency must never satisfy readiness")
		}
	}
}
