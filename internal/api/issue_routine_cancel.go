package api

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// Parked routines have no goroutine left to receive cancellation. Settle their
// durable run, approval and Inbox state in the same transaction as takeover or
// Stop. Live routines receive cancellation through the execution monitor.
func cancelParkedIssueRoutinesTx(ctx context.Context, tx *sql.Tx, missionID, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT w.token FROM pipeline_waitpoints w JOIN pipeline_runs r ON r.id=w.pipeline_run_id JOIN issue_executions x ON x.routine_run_id=r.id WHERE x.mission_id=? AND r.status='waiting' AND w.status='pending'`, missionID)
	if err != nil {
		return err
	}
	var tokens []string
	for rows.Next() {
		var token string
		if err = rows.Scan(&token); err != nil {
			rows.Close()
			return err
		}
		tokens = append(tokens, token)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if _, err = tx.ExecContext(ctx, `UPDATE pipeline_waitpoints SET status='cancelled',decided_at=? WHERE token=? AND status='pending'`, now, token); err != nil {
			return err
		}
		if _, err = inbox.ResolveBySourceTx(ctx, tx, "waitpoint", token, "cancelled", ""); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE pipeline_runs SET status='cancelled',outcome='CANCELLED',ended_at=?,updated_at=?,error_message='Issue work stopped or taken over' WHERE status='waiting' AND id IN (SELECT routine_run_id FROM issue_executions WHERE mission_id=?)`, now, now, missionID)
	return err
}
