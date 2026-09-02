package orchestrator

// Regression coverage for PRD-ISSUES-AND-ROUTINES-2026 work package A1
// ("Stop actually stops (Tier 1), and terminal states hold").
//
// Defect: OnAssignmentCompleted's UPDATE mission_tasks SET status=?... WHERE
// id=? carried no status guard, unlike the sibling write on missions
// (mission.go's finalizeMission: `...WHERE id = ? AND status = 'IN_PROGRESS'`).
// Tier 1 stop is cooperative — there is no kill primitive for a shared crew
// container (internal/provider/container.go has Exec/ExecInspect, no Kill) —
// so a run Stop asked to cancel keeps executing and eventually reports its
// own completion. Without the guard, that late report resurrected a task
// Stop had already moved to CANCELLED back to COMPLETED.
//
// TestOnAssignmentCompleted_DoesNotResurrectCancelledTask reproduces this
// against the unmodified function and is the test that must fail on main.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestOnAssignmentCompleted_DoesNotResurrectCancelledTask(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Simulate IssueHandler.Stop: the task was live, an operator stopped the
	// issue, and Stop's UPDATE mission_tasks moved it straight to CANCELLED
	// (internal/api/issue_handler_workflow.go's Stop handler).
	if _, err := db.Exec(`INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Task 1', 'CANCELLED', 1, '[]', 'assign-1', ?, ?)`,
		missionID, agentID, now, now); err != nil {
		t.Fatalf("seed cancelled task: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)
	engine.mu.Lock()
	engine.active[missionID] = &missionState{
		ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
		WorkspaceID: wsID, TraceID: "mission-trace-1",
		cancel: func() {},
	}
	engine.mu.Unlock()

	// The sub-agent run ignored (or outran) the stop and finishes on its
	// own — a late OnAssignmentCompleted for the same assignment, reporting
	// success.
	if err := engine.OnAssignmentCompleted(context.Background(), "assign-1", "COMPLETED", "late result, arrived after stop", ""); err != nil {
		t.Fatalf("OnAssignmentCompleted: %v", err)
	}

	var status, result string
	if err := db.QueryRow(`SELECT status, COALESCE(result_summary, '') FROM mission_tasks WHERE id = 't1'`).Scan(&status, &result); err != nil {
		t.Fatalf("query task: %v", err)
	}
	if status != "CANCELLED" {
		t.Errorf("task status = %q, want CANCELLED (late completion must not resurrect a stopped task)", status)
	}
	if result == "late result, arrived after stop" {
		t.Errorf("result_summary was overwritten by the late completion; a terminal task must not be mutated further")
	}
}

// TestOnAssignmentCompleted_DoesNotResurrectFailedOrCompletedTask covers the
// same guard for the other two terminal states, so a duplicate/late
// completion callback (any source, not just Stop) never flips a task that
// already reached FAILED or COMPLETED.
func TestOnAssignmentCompleted_DoesNotResurrectFailedOrCompletedTask(t *testing.T) {
	for _, terminal := range []string{"FAILED", "COMPLETED"} {
		t.Run(terminal, func(t *testing.T) {
			db := setupTestDB(t)
			wsID, crewID, leadID, agentID := seedTestData(t, db)
			missionID := createTestMission(t, db, wsID, crewID, leadID)

			now := time.Now().UTC().Format(time.RFC3339)
			if _, err := db.Exec(`INSERT INTO mission_tasks
				(id, mission_id, assigned_agent_id, title, status, task_order, depends_on, assignment_id, created_at, updated_at)
				VALUES ('t1', ?, ?, 'Task 1', ?, 1, '[]', 'assign-1', ?, ?)`,
				missionID, agentID, terminal, now, now); err != nil {
				t.Fatalf("seed task: %v", err)
			}

			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			engine := NewMissionEngine(db, nil, nil, logger)
			engine.mu.Lock()
			engine.active[missionID] = &missionState{
				ID: missionID, CrewID: crewID, CrewSlug: "dev-crew",
				WorkspaceID: wsID, TraceID: "mission-trace-1",
				cancel: func() {},
			}
			engine.mu.Unlock()

			// A duplicate completion callback for the same assignment arrives.
			if err := engine.OnAssignmentCompleted(context.Background(), "assign-1", "COMPLETED", "duplicate", ""); err != nil {
				t.Fatalf("OnAssignmentCompleted: %v", err)
			}

			var status string
			if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't1'`).Scan(&status); err != nil {
				t.Fatalf("query task: %v", err)
			}
			if status != terminal {
				t.Errorf("task status = %q, want unchanged %q", status, terminal)
			}
		})
	}
}
