package orchestrator

import (
	"log/slog"
	"testing"
)

func TestMissionTaskOutcomeDoesNotUnblockIncompleteWork(t *testing.T) {
	for _, tc := range []struct{ outcome, want string }{
		{OutcomeSucceeded, "COMPLETED"}, {OutcomeNoChange, "COMPLETED"}, {OutcomeWorkCreated, "COMPLETED"},
		{OutcomeNeedsHuman, "AWAITING_APPROVAL"}, {OutcomePartial, "FAILED"}, {OutcomeFailed, "FAILED"}, {OutcomeCancelled, "CANCELLED"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			ctx := t.Context()
			db := covMissionDB(t)
			ws, crew, lead, agent := seedTestData(t, db)
			if _, err := db.ExecContext(ctx, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,title,status) VALUES('m',?,?,?,'Goal','IN_PROGRESS')`, ws, crew, lead); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO assignments(id,workspace_id,assigned_to_id,status,outcome) VALUES('run',?,?,'COMPLETED',?)`, ws, agent, tc.outcome); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO mission_tasks(id,mission_id,title,status,assignment_id,assigned_agent_id) VALUES('first','m','first','IN_PROGRESS','run',?)`, agent); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO mission_tasks(id,mission_id,title,status,depends_on) VALUES('next','m','next','BLOCKED','["first"]')`); err != nil {
				t.Fatal(err)
			}
			engine := NewMissionEngine(db, nil, nil, slog.Default())
			if err := engine.OnAssignmentCompleted(ctx, "run", "COMPLETED", "Result", ""); err != nil {
				t.Fatal(err)
			}
			var status, next string
			if err := db.QueryRowContext(ctx, `SELECT status FROM mission_tasks WHERE id='first'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != tc.want {
				t.Fatalf("task status=%s want %s", status, tc.want)
			}
			if err := db.QueryRowContext(ctx, `SELECT status FROM mission_tasks WHERE id='next'`).Scan(&next); err != nil {
				t.Fatal(err)
			}
			if tc.want == "COMPLETED" && next == "BLOCKED" {
				t.Fatal("successful work did not unblock the dependent task")
			}
			if tc.want == "AWAITING_APPROVAL" {
				var completedEvents int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_activity WHERE mission_id='m' AND action='task_completed'`).Scan(&completedEvents); err != nil || completedEvents != 0 {
					t.Fatalf("input request reported task completion: %d %v", completedEvents, err)
				}
			}
			if tc.want != "COMPLETED" && next != "BLOCKED" {
				t.Fatalf("incomplete result unblocked dependent: %s", next)
			}
		})
	}
}
