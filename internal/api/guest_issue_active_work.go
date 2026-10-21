package api

import (
	"context"
	"database/sql"
)

// Legacy mission-task assignments can have no assignments.mission_id. Inspect
// both edges and unassigned pending tasks before changing Guest intake work.
// Superseding an execution does not terminate its routine. Check the linked
// run independently of execution stage, in the mutation's transaction.
func guestIssueActiveWorkIn(ctx context.Context, tx *sql.Tx, issue string) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM assignments WHERE (mission_id=? OR chat_id=? OR group_id=?) AND status NOT IN ('COMPLETED','FAILED','CANCELLED'))
 OR EXISTS(SELECT 1 FROM mission_tasks t JOIN assignments a ON a.id=t.assignment_id WHERE t.mission_id=? AND a.status NOT IN ('COMPLETED','FAILED','CANCELLED'))
 OR EXISTS(SELECT 1 FROM mission_tasks WHERE mission_id=? AND status NOT IN ('COMPLETED','FAILED','CANCELLED','SKIPPED'))
 OR EXISTS(SELECT 1 FROM issue_executions WHERE mission_id=? AND stage IN ('working','reviewing'))
 OR EXISTS(SELECT 1 FROM issue_executions x JOIN pipeline_runs pr ON pr.id=x.routine_run_id
 WHERE x.mission_id=? AND pr.status IN ('queued','running','waiting'))`, issue, issue, issue, issue, issue, issue, issue).Scan(&active)
	return active, err
}
