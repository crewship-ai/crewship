package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrRecoveredStopConflict means a lifecycle outcome and an audit-only stop
// would contradict one another. Retrying the same entry cannot resolve it.
var ErrRecoveredStopConflict = errors.New("journal: recovered stop conflicts with recorded lifecycle")

// HasRecoveredStop binds the lookup to the complete confirmed identity.
func HasRecoveredStop(ctx context.Context, db *sql.DB, workspace, agent, run string) (bool, error) {
	var found bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries
 WHERE workspace_id=? AND agent_id=? AND trace_id=? AND entry_type='run.recovered_stop')`, workspace, agent, run).Scan(&found)
	return found, err
}

// checkRecoveredStopWrite runs INSIDE the insert transaction, including batch
// writes. A preflight query alone permits a delayed start to race recovery.
func checkRecoveredStopWrite(ctx context.Context, tx *sql.Tx, e Entry) error {
	var conflict bool
	switch e.Type {
	case EntryRunRecoveredStop:
		// A run ID with contradictory ownership is not proof of a recovered stop.
		// Also prevent duplicate audits submitted with a different entry ID.
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries
 WHERE trace_id=? AND entry_type IN ('run.started','run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop'))`, e.TraceID).Scan(&conflict)
		if err != nil {
			return err
		}
	case EntryRunStarted, EntryRunCompleted, EntryRunFailed, EntryRunCancelled, EntryRunTimeout:
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries
 WHERE workspace_id=? AND agent_id=? AND trace_id=? AND entry_type='run.recovered_stop')`, e.WorkspaceID, e.AgentID, e.TraceID).Scan(&conflict)
		if err != nil {
			return err
		}
	default:
		return nil
	}
	if conflict {
		return fmt.Errorf("%w: %s", ErrRecoveredStopConflict, e.Type)
	}
	return nil
}

// Only internal, fixed SQL aliases are passed here. The scoped anti-join also
// covers late historical imports without turning an audit into a normal run.
func withoutRecoveredStop(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM journal_entries recovered_audit WHERE recovered_audit.entry_type='run.recovered_stop'
 AND recovered_audit.workspace_id=` + alias + `.workspace_id AND recovered_audit.agent_id=` + alias + `.agent_id
 AND recovered_audit.trace_id=` + alias + `.trace_id)`
}
