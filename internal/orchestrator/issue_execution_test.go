package orchestrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func executionFixture(t *testing.T) (*MissionEngine, *missionState, *sql.DB) {
	t.Helper()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "issue", "IN_PROGRESS")
	mustExec(t, db, `DROP TABLE issue_executions;
 CREATE TABLE issue_executions(id TEXT PRIMARY KEY,mission_id TEXT,work_revision INTEGER,brief_revision INTEGER,attempt INTEGER DEFAULT 0,stage TEXT,reviewer_agent_id TEXT,review_task_id TEXT,review_note TEXT,verdict TEXT,routine_run_id TEXT,created_at TEXT,updated_at TEXT);
 CREATE UNIQUE INDEX active_execution ON issue_executions(mission_id) WHERE stage IN ('working','reviewing');
 CREATE TABLE issue_work(mission_id TEXT PRIMARY KEY,revision INTEGER,brief_revision INTEGER,mode TEXT,client_review_required INTEGER);
 ALTER TABLE assignments ADD COLUMN issue_execution_id TEXT;
 ALTER TABLE mission_tasks ADD COLUMN issue_execution_id TEXT;
 INSERT INTO issue_work VALUES('issue',0,0,'agent',0);
 INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at) VALUES('execution','issue',0,0,'working','agent-lead','2026-09-08');
 INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,status,outcome,result_summary,issue_execution_id,created_at)
 VALUES('worker','ws-1','issue','agent-lead','agent-worker','COMPLETED','SUCCEEDED','Verified output','execution','2026-09-08');`)
	return newLifecycleEngine(t, db), ms, db
}
func finishReview(t *testing.T, db *sql.DB, verdict, outcome string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,status,outcome,result_summary,issue_execution_id,created_at)
 VALUES('review','ws-1','issue','agent-lead','agent-lead','COMPLETED',?,?,'execution','2026-09-09')`, outcome, "---HANDOFF---\nsummary: Verified the output; fix the missing assertion\nconfidence: high\noutcome: "+outcome+"\nreview: "+verdict+"\nreview_target: bob\n---END HANDOFF---")
	mustExec(t, db, `UPDATE mission_tasks SET assignment_id='review',status='COMPLETED' WHERE id=(SELECT review_task_id FROM issue_executions WHERE id='execution')`)
}
func TestIssueExecutionReviewSurvivesRestartAndRequiresVerdict(t *testing.T) {
	e, ms, db := executionFixture(t)
	for i := 0; i < 2; i++ {
		if _, err := e.checkIssueExecution(context.Background(), ms); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM mission_tasks`).Scan(&count)
	if count != 1 {
		t.Fatalf("duplicate review tasks: %d", count)
	}
	finishReview(t, db, "approve", OutcomeSucceeded)
	// A fresh engine has no planning state; the durable review still completes.
	e = newLifecycleEngine(t, db)
	if _, err := e.checkIssueExecution(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	var status string
	db.QueryRow(`SELECT status FROM missions WHERE id='issue'`).Scan(&status)
	if status != "DONE" {
		t.Fatalf("status %s", status)
	}
}
func TestIssueExecutionChangesCreateNewWorkAndKeepHistory(t *testing.T) {
	e, ms, db := executionFixture(t)
	if _, err := e.checkIssueExecution(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	finishReview(t, db, "request_changes", OutcomeSucceeded)
	if _, err := e.checkIssueExecution(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	var stage, note string
	db.QueryRow(`SELECT stage FROM issue_executions WHERE id='execution'`).Scan(&stage)
	if stage != "changes_requested" {
		t.Fatal(stage)
	}
	db.QueryRow(`SELECT description FROM mission_tasks WHERE status='PENDING'`).Scan(&note)
	if !strings.Contains(note, "missing assertion") {
		t.Fatalf("correction did not receive review: %s", note)
	}
	var old int
	db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id IN ('worker','review') AND status='COMPLETED'`).Scan(&old)
	if old != 2 {
		t.Fatal("history lost")
	}
}
func TestIssueExecutionRejectsMissingOutcomeAndStaleApproval(t *testing.T) {
	for _, kind := range []string{"missing outcome", "stale brief", "failed worker"} {
		t.Run(kind, func(t *testing.T) {
			e, ms, db := executionFixture(t)
			if _, err := e.checkIssueExecution(context.Background(), ms); err != nil {
				t.Fatal(err)
			}
			outcome := OutcomeSucceeded
			if kind == "missing outcome" {
				outcome = OutcomeFailed
			}
			finishReview(t, db, "approve", outcome)
			if kind == "stale brief" {
				mustExec(t, db, `UPDATE issue_work SET brief_revision=1`)
			}
			if kind == "failed worker" {
				mustExec(t, db, `UPDATE assignments SET outcome='FAILED' WHERE id='worker'`)
			}
			if _, err := e.checkIssueExecution(context.Background(), ms); err != nil {
				t.Fatal(err)
			}
			var status string
			db.QueryRow(`SELECT status FROM missions WHERE id='issue'`).Scan(&status)
			if status == "DONE" {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
