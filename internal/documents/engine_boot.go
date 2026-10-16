package documents

import (
	"context"
	"errors"
)

// RecoverEngine is a service-only, idempotent boot barrier. The Node engine
// invokes it only after obtaining its exclusive configured private listener.
func (s *Store) RecoverEngine(ctx context.Context, boot string) error {
	if len(boot) < 16 || len(boot) > 128 {
		return errors.New("invalid engine boot identity")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE document_writer_fence SET seq=seq WHERE id=1`); e != nil {
		return e
	}
	var previous string
	if e = tx.QueryRowContext(ctx, `SELECT boot_id FROM document_writer_fence WHERE id=1`).Scan(&previous); e != nil {
		return e
	}
	if previous == boot {
		return tx.Commit()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE documents SET epoch=epoch+1,generation=generation+1,writer='',owner='',lease_until=0`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE document_writer_fence SET boot_id=? WHERE id=1`, boot); e != nil {
		return e
	}
	return tx.Commit()
}
