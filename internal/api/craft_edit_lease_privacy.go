package api

import (
	"context"
	"database/sql"
	"fmt"
)

// eraseCraftEditLeaseIdentity runs inside the Pages identity-erasure transaction.
// Retain generation tombstones: deleting a lease would let a later acquisition
// reuse a generation that an erased browser still holds. A host may bind human
// actors as the canonical user ID or its explicit user:<id> form.
func eraseCraftEditLeaseIdentity(ctx context.Context, tx *sql.Tx, workspaceID, userID string) error {
	prefixed := "user:" + userID
	// Clear requests before the holder identity used by this predicate goes.
	// Every request against an invalidated holder generation is obsolete, even
	// when its requester is somebody whose identity is not being erased.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM mooring_edit_lease_requests
		 WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?)
		   AND (actor_id IN (?,?) OR page_id IN (
		       SELECT page_id FROM mooring_edit_leases WHERE actor_id IN (?,?)))`,
		workspaceID, userID, prefixed, userID, prefixed); err != nil {
		return fmt.Errorf("erase Craft edit lease requests: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE mooring_edit_leases
		   SET actor_id='',session_id='',expires_ns=0
		 WHERE actor_id IN (?,?)
		   AND page_id IN (SELECT id FROM pages WHERE workspace_id=?)`,
		userID, prefixed, workspaceID); err != nil {
		return fmt.Errorf("erase Craft edit lease holder: %w", err)
	}
	return nil
}
