package api

import (
	"context"
	"database/sql"
)

// releaseCredentialNameIn frees only a retired name in the same transaction as
// its replacement INSERT. The complete original name remains recoverable by
// stripping the exact suffix derived from this row's unchanged ID. A collision
// refuses the whole transaction; it never deletes or truncates history.
func releaseCredentialNameIn(ctx context.Context, tx *sql.Tx, workspaceID, name string) error {
	_, err := tx.ExecContext(ctx, `UPDATE credentials SET name = name || ' [retired:' || id || ']'
 WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL`, workspaceID, name)
	return err
}
