package api

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

type folderDeletionAuditFrame struct {
	id     string
	native []any
}

// The configured AppendTx already checks the prepared keyed INSERT. Retain
// its exact native row so a later append cannot alter an earlier deletion
// event. This reads only IDs created by this folder deletion, not history.
func captureFolderDeletionAudit(ctx context.Context, tx *sql.Tx, id string) (folderDeletionAuditFrame, error) {
	frame := folderDeletionAuditFrame{id: id}
	rows, err := tx.QueryContext(ctx, `SELECT * FROM journal_entries WHERE id=?`, id)
	if err != nil {
		return frame, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return frame, err
	}
	if !rows.Next() {
		return frame, errors.Join(sql.ErrNoRows, rows.Err())
	}
	frame.native = make([]any, len(columns))
	targets := make([]any, len(columns))
	for i := range targets {
		targets[i] = &frame.native[i]
	}
	if err := rows.Scan(targets...); err != nil {
		return frame, err
	}
	if rows.Next() {
		return frame, errors.New("folder deletion audit identity is not unique")
	}
	return frame, errors.Join(rows.Err(), rows.Close())
}

func acknowledgeFolderDeletionAudits(ctx context.Context, tx *sql.Tx, frames []folderDeletionAuditFrame) error {
	for _, expected := range frames {
		actual, err := captureFolderDeletionAudit(ctx, tx, expected.id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual.native, expected.native) {
			return errors.New("folder deletion audit did not survive unchanged")
		}
	}
	return nil
}
