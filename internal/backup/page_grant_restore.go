package backup

import (
	"context"
	"database/sql"
)

// A retired Page grant stays retired (#5069), and a restore must not be the way
// back. page_grant_retired_issuances remembers every retired issuance on this
// target, past revoke and reissue, and is outside the backup, so a replacing
// restore neither removes it nor brings a copy. Each imported grant meets it
// here. A new issuance has its own granted_at and is not matched.
//
// The retirement triggers do not see a restored row: its grant arrives after its
// issuer's membership. A bundle made before retirement existed can carry a grant
// whose issuer had no standing, so the restored set is judged once, in the same
// transaction. A bundle without memberships is not judged: nothing in it says
// who has standing, and the target may receive its members from a later restore.
func retireRestoredPageGrants(ctx context.Context, tx *sql.Tx, dump *DBDump) error {
	if has, err := tableHasColumn(ctx, tx, "page_grants", "retired_at"); err != nil || !has {
		return err
	}
	for _, g := range dump.Tables["page_grants"] {
		if _, err := tx.ExecContext(ctx, `UPDATE page_grants SET retired_at=(SELECT t.retired_at FROM page_grant_retired_issuances t WHERE t.page_id=page_grants.page_id AND t.subject_type=page_grants.subject_type AND t.subject_id=page_grants.subject_id AND t.level=page_grants.level AND t.granted_by_user_id=page_grants.granted_by_user_id AND t.granted_at=page_grants.granted_at)
			WHERE page_id=? AND subject_type=? AND subject_id=? AND level=? AND granted_by_user_id=? AND granted_at=? AND retired_at IS NULL
			AND EXISTS(SELECT 1 FROM page_grant_retired_issuances t WHERE t.page_id=page_grants.page_id AND t.subject_type=page_grants.subject_type AND t.subject_id=page_grants.subject_id AND t.level=page_grants.level AND t.granted_by_user_id=page_grants.granted_by_user_id AND t.granted_at=page_grants.granted_at)`,
			g["page_id"], g["subject_type"], g["subject_id"], g["level"], g["granted_by_user_id"], g["granted_at"]); err != nil {
			return err
		}
	}
	if len(dump.Tables["workspace_members"]) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0)`)
	return err
}
