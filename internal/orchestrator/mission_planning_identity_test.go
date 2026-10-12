package orchestrator

import (
	"context"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestLeadPlanningIdentitySurvivesRestartAndConcurrentAdmission(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	workspaceID, crewID, leadID, _ := fkMissionSeed(t, db)
	original := fkMission(t, db, "planning-restart", workspaceID, crewID, leadID, "IN_PROGRESS")
	// No dispatcher: acceptance commits, then the process disappears before
	// any wake-up is delivered or any mission tasks are created.
	if err := fkEngine(t, db).dispatchLeadPlanning(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	var firstID, firstInput string
	if err := db.QueryRow(`SELECT a.id,w.input_json FROM assignments a JOIN work_items w ON w.domain_id=a.id AND w.source='assignment' WHERE a.mission_id=?`, original.ID).Scan(&firstID, &firstInput); err != nil {
		t.Fatal(err)
	}
	// Simulate changed mutable prompt data and a brand-new engine/state per
	// caller. Neither process-local flags nor prompt equality prevent duplicates.
	fkMissionExec(t, db, `UPDATE missions SET title='renamed after crash' WHERE id=?`, original.ID)
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state := &missionState{ID: original.ID, WorkspaceID: workspaceID, CrewID: crewID, CrewSlug: original.CrewSlug, LeadAgentID: leadID, TraceID: original.TraceID, cancel: func() {}}
			errs <- fkEngine(t, db).dispatchLeadPlanning(context.Background(), state)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var assignments, items int
	if err := db.QueryRow(`SELECT count(*) FROM assignments WHERE mission_id=?`, original.ID).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE source='assignment'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if assignments != 1 || items != 1 {
		t.Fatalf("restart created assignments=%d work=%d", assignments, items)
	}
	var persistedID, input string
	if err := db.QueryRow(`SELECT m.planning_assignment_id,w.input_json FROM missions m JOIN work_items w ON w.domain_id=m.planning_assignment_id AND w.source='assignment' WHERE m.id=?`, original.ID).Scan(&persistedID, &input); err != nil {
		t.Fatal(err)
	}
	if persistedID != firstID || input != firstInput {
		t.Fatal("restart replaced durable planning identity or immutable input")
	}
	// A terminal planning assignment also cannot be automatically resurrected
	// merely because it never created tasks.
	fkMissionExec(t, db, `UPDATE assignments SET status='FAILED' WHERE id=?`, firstID)
	if err := fkEngine(t, db).dispatchLeadPlanning(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM assignments WHERE mission_id=?`, original.ID).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if assignments != 1 {
		t.Fatal("terminal planner was re-admitted")
	}
}

func TestLeadPlanningIdentityRollsBackWithAdmission(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	workspaceID, crewID, leadID, _ := fkMissionSeed(t, db)
	state := fkMission(t, db, "planning-rollback", workspaceID, crewID, leadID, "IN_PROGRESS")
	fkMissionExec(t, db, `CREATE TRIGGER reject_planning_work BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'injected admission failure'); END`)
	if err := fkEngine(t, db).dispatchLeadPlanning(t.Context(), state); err == nil {
		t.Fatal("injected admission failure accepted")
	}
	var assignments, reservations int
	if err := db.QueryRow(`SELECT count(*) FROM assignments`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM missions WHERE planning_assignment_id IS NOT NULL`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if assignments != 0 || reservations != 0 {
		t.Fatalf("partial admission assignments=%d reservations=%d", assignments, reservations)
	}
	fkMissionExec(t, db, `DROP TRIGGER reject_planning_work`)
	if err := fkEngine(t, db).dispatchLeadPlanning(t.Context(), state); err != nil {
		t.Fatal(err)
	}
}

// A tick's in-memory status can predate Stop or human takeover. Both commit
// their mission status change before a stale planning transaction can reserve
// a new identity; no assignment escapes the cancellation transaction's scan.
func TestLeadPlanningIdentityRejectsStaleStoppedOrHumanTick(t *testing.T) {
	for _, status := range []string{"CANCELLED", "TODO", "PAUSED", "COMPLETED"} {
		t.Run(status, func(t *testing.T) {
			db := testutil.MigratedSQLDB(t)
			workspaceID, crewID, leadID, _ := fkMissionSeed(t, db)
			state := fkMission(t, db, "planning-stopped", workspaceID, crewID, leadID, "IN_PROGRESS")
			fkMissionExec(t, db, `UPDATE missions SET status=? WHERE id=?`, status, state.ID)
			if err := fkEngine(t, db).dispatchLeadPlanning(t.Context(), state); err == nil {
				t.Fatal("stale tick admitted work after mission authority changed")
			}
			var assignments, reservations int
			if err := db.QueryRow(`SELECT count(*) FROM assignments`).Scan(&assignments); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM missions WHERE planning_assignment_id IS NOT NULL`).Scan(&reservations); err != nil {
				t.Fatal(err)
			}
			if assignments != 0 || reservations != 0 {
				t.Fatalf("stopped mission admitted assignments=%d reservations=%d", assignments, reservations)
			}
		})
	}
}
