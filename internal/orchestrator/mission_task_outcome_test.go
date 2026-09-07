package orchestrator

import (
	"context"
	"log/slog"
	"testing"
)

func TestMissionTaskOutcomeDoesNotUnblockIncompleteWork(t *testing.T) {
	for _, tc := range []struct{ outcome, want string }{
		{OutcomeSucceeded, "COMPLETED"}, {OutcomeNoChange, "COMPLETED"}, {OutcomeWorkCreated, "COMPLETED"},
		{OutcomeNeedsHuman, "AWAITING_APPROVAL"}, {OutcomePartial, "FAILED"}, {OutcomeFailed, "FAILED"}, {OutcomeCancelled, "CANCELLED"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, lead, agent := seedTestData(t, db)
			if _, err := db.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,title,status) VALUES('m',?,?,?,'Goal','IN_PROGRESS')`, ws, crew, lead); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO assignments(id,workspace_id,assigned_to_id,status,outcome) VALUES('run',?,?,'COMPLETED',?)`, ws, agent, tc.outcome); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,assignment_id) VALUES('first','m','first','IN_PROGRESS','run')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,depends_on) VALUES('next','m','next','BLOCKED','["first"]')`); err != nil {
				t.Fatal(err)
			}
			engine := NewMissionEngine(db, nil, nil, slog.Default())
			if err := engine.OnAssignmentCompleted(context.Background(), "run", "COMPLETED", "Result", ""); err != nil {
				t.Fatal(err)
			}
			var status, next string
			if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id='first'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != tc.want {
				t.Fatalf("task status=%s want %s", status, tc.want)
			}
			if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id='next'`).Scan(&next); err != nil {
				t.Fatal(err)
			}
			if tc.want != "COMPLETED" && next != "BLOCKED" {
				t.Fatalf("incomplete result unblocked dependent: %s", next)
			}
		})
	}
}
