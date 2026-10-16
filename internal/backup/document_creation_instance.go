package backup

import (
	"context"
	"database/sql"
	"fmt"
)

// A physical bundle copies runtime rows too. Creation consent and its original
// run capability belong to the source installation; durable creation provenance
// remains historical. Offline recovery owns the DB before any server can open it.
func resetPhysicalDocumentCreationAuthority(ctx context.Context, db *sql.DB) (bool, error) {
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='document_creation_consents')`).Scan(&present); err != nil {
		return false, err
	}
	// Older bundles have no creation capability.
	if !present {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Delete only the runs bound to the source's typed creation consent. Other
	// run origins retain their existing recovery policy and historical identity.
	for _, table := range []string{"page_artifact_run_delegations", "artifact_run_principals"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE run_id IN (SELECT run_id FROM document_creation_consents WHERE run_id IS NOT NULL)`); err != nil {
			return false, fmt.Errorf("backup: reset document creation run authority: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM document_creation_consents`); err != nil {
		return false, fmt.Errorf("backup: reset document creation consent: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
