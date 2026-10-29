package backup

import (
	"context"
	"database/sql"
)

// Physical recovery retains every issued generation but retires its runtime
// principal, including on the same OS boot. Deleting the row would allow a
// delayed generation to recur after fresh admission. Portable restore never
// carries these rows and refuses to discard a target's existing tombstones.
func resetRecoveredCraftEditLeases(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"mooring_edit_lease_requests", "mooring_edit_leases"} {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		query := `DELETE FROM mooring_edit_lease_requests`
		if table == "mooring_edit_leases" {
			query = `UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0`
		}
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}
