package mooring

import (
	"context"
	"database/sql"
)

// Release is a trusted host lifecycle operation, never participant authority.
// The manager must stop/drain the actual old runtime before letting another
// process use its scope. Expiring an owner does NOT acknowledge any outstanding
// delivery lease: original gate expiry/barrier evidence remains untouched.
// A fresh Acquire advances the retained generation and issues a fresh token.
func (s *OwnerStore) Release(ctx context.Context, lease OwnerLease) error {
	return s.transaction(ctx, func(tx *sql.Tx, boot string, now int64) error {
		if err := s.check(ctx, tx, lease, boot, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mooring_session_owners SET expires_ns=? WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND generation=? AND token_hash=?`, now, lease.Key.ArtifactID, lease.Key.WorkspaceID, lease.Key.ApplicationID, lease.OwnerID, lease.Generation, ownerTokenHash(lease.Token)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1 WHERE page_id=? AND workspace_id=? AND application_id=? AND owner_id=? AND owner_generation=? AND owner_token_hash=?`, lease.Key.ArtifactID, lease.Key.WorkspaceID, lease.Key.ApplicationID, lease.OwnerID, lease.Generation, ownerTokenHash(lease.Token))
		return err
	})
}
