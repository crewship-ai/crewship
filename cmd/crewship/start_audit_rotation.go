//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/journal"
)

// pendingRotationLookup reads the instance audit entry that decides a staged
// key rotation: the key it names and whether it is the newest rotation entry.
func pendingRotationLookup(ctx context.Context, db *sql.DB) func(string) (journal.PendingRotation, error) {
	return func(id string) (journal.PendingRotation, error) {
		var record journal.PendingRotation
		var active sql.NullString
		err := db.QueryRowContext(ctx, `SELECT r.entity_id, r.id = (SELECT n.id FROM instance_audit_logs n WHERE n.action = ? ORDER BY COALESCE(n.chain_seq, 0) DESC, n.rowid DESC LIMIT 1)
			FROM instance_audit_logs r WHERE r.id = ? AND r.action = ?`,
			journal.SigningKeyRotatedAction, id, journal.SigningKeyRotatedAction).Scan(&active, &record.Newest)
		if errors.Is(err, sql.ErrNoRows) {
			return record, nil
		}
		if err != nil {
			return record, err
		}
		record.Found, record.ActiveKeyID = true, active.String
		return record, nil
	}
}
