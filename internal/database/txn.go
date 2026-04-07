package database

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTx executes fn inside a transaction. If fn returns an error the
// transaction is rolled back; otherwise it is committed. The caller
// must NOT call Commit or Rollback inside fn.
func WithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// WithTxResult is like WithTx but returns a value from fn.
func WithTxResult[T any](ctx context.Context, db *sql.DB, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	result, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
