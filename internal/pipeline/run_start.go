package pipeline

import (
	"context"
	"database/sql"
	"fmt"
)

const startPhasePreHooks = "pre_hooks"

// The row and phase become visible together. A missing row therefore proves
// that neither setup hooks nor the body passed the durable start boundary.
func (s *RunStore) insertBeforeHooks(ctx context.Context, record *RunRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insert(ctx, tx, record); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET start_phase=? WHERE id=?`, startPhasePreHooks, record.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *RunStore) startPhase(ctx context.Context, runID string) (string, error) {
	var phase sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT start_phase FROM pipeline_runs WHERE id=?`, runID).Scan(&phase)
	return phase.String, err
}

func (s *RunStore) enterBody(ctx context.Context, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE pipeline_runs SET start_phase='body' WHERE id=? AND start_phase='pre_hooks' AND status='running'`, runID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("routine %s cannot enter body: durable start is no longer active", runID)
	}
	return nil
}
