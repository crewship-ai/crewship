package backup

import (
	"context"
	"database/sql"
	"fmt"
)

// reconcile.go — pre-INSERT alignment of bundle identities with
// target identities when a UNIQUE-constrained column would otherwise
// collide.
//
// The canonical case: `users.email` is UNIQUE. If the bundle carries
// `u_admin_old (admin@e2e.test)` and the target already has
// `u_admin_new (admin@e2e.test)`, naive INSERT OR IGNORE drops the
// bundle row on the UNIQUE collision — leaving the bundle's
// `crew_members.user_id = u_admin_old` pointing at a row that does
// not (and will never) exist on the target. Restore aborts on the
// deferred FK check.
//
// ReconcileUsersByEmail solves this by:
//
//	1. Looking up each bundle user's email on the target.
//	2. When the target has the same email under a DIFFERENT id,
//	   record bundle_id → target_id and rewrite the bundle's user
//	   row to use target_id (so INSERT OR IGNORE no-ops cleanly).
//	3. Discover every FK column on every table that REFERENCES
//	   users.id, walk the dump, and rewrite each referenced bundle
//	   user_id to its mapped target_id.
//
// Runs INSIDE the restore transaction (via the PreInsert hook) so
// the email lookup sees the post-Replace target state.
//
// Runs unconditionally on every restore (--replace or not). The
// canonical "restore into a different instance" scenario benefits
// from the same alignment.

// ReconcileUsersByEmail rewrites bundle user IDs (and dependent FKs)
// to match any target user with the same email. Returns the
// bundle_id → target_id map for caller observability (logging,
// stats). A nil dump or absent users table is a no-op.
func ReconcileUsersByEmail(ctx context.Context, tx *sql.Tx, dump *DBDump) (map[string]string, error) {
	if dump == nil {
		return nil, nil
	}
	users := dump.Tables["users"]
	if len(users) == 0 {
		return nil, nil
	}
	exists, err := tableExistsTx(ctx, tx, "users")
	if err != nil {
		return nil, fmt.Errorf("backup: probe users: %w", err)
	}
	if !exists {
		// Target lacks the users table (different schema variant).
		// Nothing to align against.
		return nil, nil
	}

	remap := map[string]string{}
	for _, u := range users {
		email, _ := u["email"].(string)
		bundleID, _ := u["id"].(string)
		if email == "" || bundleID == "" {
			continue
		}
		var targetID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&targetID)
		if err == sql.ErrNoRows {
			// Target has no user with this email — bundle row will
			// land cleanly with its original id. No remap needed.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("backup: lookup target user by email: %w", err)
		}
		if targetID == bundleID {
			// Same id, same email — bundle and target already aligned
			// (e.g. restoring into the source instance). No-op.
			continue
		}
		// Different id, same email: the conflict scenario. Rewrite
		// the bundle's user row in-place so the upcoming INSERT OR
		// IGNORE sees a matching target id (no-ops correctly), and
		// record the remap for FK rewrites below.
		remap[bundleID] = targetID
		u["id"] = targetID
	}
	if len(remap) == 0 {
		return nil, nil
	}

	// Find every FK column on every dumped table that REFERENCES
	// users.id, then rewrite. PRAGMA introspection inside the tx so
	// schema drift between source and target is handled: a column
	// the bundle has but the target dropped just won't appear in
	// foreign_key_list and gets left alone (the INSERT pass already
	// filters unknown columns via tableColumns).
	for table, rows := range dump.Tables {
		if len(rows) == 0 || table == "users" {
			continue
		}
		edges, err := tableFKEdgesTx(ctx, tx, table)
		if err != nil {
			// Table doesn't exist on target — skip. Real driver
			// errors will surface on the INSERT pass.
			continue
		}
		var userColumns []string
		for _, e := range edges {
			if e.ToTable == "users" {
				userColumns = append(userColumns, e.FromColumn)
			}
		}
		if len(userColumns) == 0 {
			continue
		}
		for _, row := range rows {
			for _, col := range userColumns {
				oldVal, ok := row[col].(string)
				if !ok || oldVal == "" {
					continue
				}
				if newVal, hit := remap[oldVal]; hit {
					row[col] = newVal
				}
			}
		}
	}
	return remap, nil
}
