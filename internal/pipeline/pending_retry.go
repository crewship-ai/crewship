package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// requeueUnstartedClaim is called only after this claim's executor returned.
// The still-owned claim attempt, rather than a matching run ID alone, fences
// cleanup. Release its pre-start reservation and requeue in the same commit.
func (s *PendingRunStore) requeueUnstartedClaim(ctx context.Context, pr PendingRun, reason string, next *time.Time) error {
	db, ok := s.db.(*sql.DB)
	if !ok {
		return errors.New("pending retry requires a live database handle")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the writer and prove this exact unlinked claim still belongs
	// to the returned executor before inspecting or deleting reservations.
	locked, err := tx.ExecContext(ctx, `UPDATE pending_runs SET status=status
 WHERE id=? AND workspace_id=? AND pipeline_id=? AND run_id=? AND status='fired'
 AND COALESCE(fired_run_id,'')='' AND dispatch_attempts=? AND fire_at=?`,
		pr.ID, pr.WorkspaceID, pr.PipelineID, pr.RunID, pr.DispatchAttempts, formatRFC3339(pr.FireAt))
	if err != nil {
		return err
	}
	n, err := locked.RowsAffected()
	if err != nil || n != 1 {
		return err // A stale callback has no cleanup authority.
	}
	var started bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_runs WHERE id=? AND workspace_id=? AND pipeline_id=?)`, pr.RunID, pr.WorkspaceID, pr.PipelineID).Scan(&started); err != nil {
		return err
	}
	if started {
		// A commit may have survived an interrupted acknowledgement. A real
		// run can have effects; retain its reservation and only link it.
		if err := NewPendingRunStoreTx(tx).SetFiredRunID(ctx, pr, pr.RunID); err != nil {
			return err
		}
		return tx.Commit()
	}
	key := pr.IdempotencyKey
	if key == "" {
		key = ScheduledFireIdempotencyKey("pending", pr.ID, pr.FireAt.UTC().Format(time.RFC3339Nano))
	}
	var owner string
	lookupErr := tx.QueryRowContext(ctx, `SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id=? AND pipeline_id=? AND idempotency_key=?`, pr.WorkspaceID, pr.PipelineID, key).Scan(&owner)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	if lookupErr == nil && owner != pr.RunID {
		_, err := tx.ExecContext(ctx, `UPDATE pending_runs SET reason_code='recovery_unknown',last_error='Dispatch reservation belongs to another identity; inspect before retrying.',next_attempt_at=NULL WHERE id=?`, pr.ID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_run_idempotency WHERE workspace_id=? AND pipeline_id=? AND idempotency_key=? AND run_id=?`, pr.WorkspaceID, pr.PipelineID, key, pr.RunID); err != nil {
		return err
	}
	if err := NewPendingRunStoreTx(tx).FinishDispatchError(ctx, pr, "pending", reason, next); err != nil {
		return err
	}
	return tx.Commit()
}
