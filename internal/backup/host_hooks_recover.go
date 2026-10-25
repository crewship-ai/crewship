package backup

import (
	"context"
	"database/sql"
	"fmt"
)

// resetRecoveredHostHooks operates before a recovered instance can start.
// Older snapshots may defer migrations until their encryption key is supplied.
func resetRecoveredHostHooks(ctx context.Context, db *sql.DB) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	exists, err := tableExists(ctx, tx, "hooks_config")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, tx.Commit()
	}
	hasApproval, err := tableHasColumn(ctx, tx, "hooks_config", "host_authorization")
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
 SELECT lower(hex(randomblob(16))),workspace_id,NULL,'hook.host_review_required','hook',id,
 '{"reason":"recovered host authority requires local review"}',strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM hooks_config WHERE handler_kind='shell'`)
	if err != nil {
		return 0, fmt.Errorf("backup: audit recovered host hook review: %w", err)
	}
	query := `UPDATE hooks_config SET enabled=0`
	if hasApproval {
		query += `,host_authorization=''`
	}
	query += ` WHERE handler_kind='shell'`
	result, err := tx.ExecContext(ctx, query)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
