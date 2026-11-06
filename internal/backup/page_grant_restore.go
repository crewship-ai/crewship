package backup

import (
	"context"
	"database/sql"
)

// A retired Page grant stays retired (#5069), and a restore must not be the way
// back. A replacing restore deletes the target's rows, so the bundle's earlier
// copy of a grant retired here would land live: the identity of each retired
// issuance is read first and applied again afterwards. A new issuance has its
// own granted_at and is not matched.
//
// The retirement triggers do not see a restored row: its grant arrives after its
// issuer's membership. A bundle made before retirement existed can carry a grant
// whose issuer had no standing, so the restored set is judged once, in the same
// transaction. A bundle without memberships is not judged: nothing in it says
// who has standing, and the target may receive its members from a later restore.

type retiredPageGrant struct {
	page, subjectType, subject, level, issuer, grantedAt, retiredAt string
}

func captureRetiredPageGrants(ctx context.Context, tx *sql.Tx) ([]retiredPageGrant, error) {
	if has, err := tableHasColumn(ctx, tx, "page_grants", "retired_at"); err != nil || !has {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at FROM page_grants WHERE retired_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []retiredPageGrant
	for rows.Next() {
		var g retiredPageGrant
		if err := rows.Scan(&g.page, &g.subjectType, &g.subject, &g.level, &g.issuer, &g.grantedAt, &g.retiredAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func retireRestoredPageGrants(ctx context.Context, tx *sql.Tx, dump *DBDump, retired []retiredPageGrant) error {
	if has, err := tableHasColumn(ctx, tx, "page_grants", "retired_at"); err != nil || !has {
		return err
	}
	for _, g := range retired {
		if _, err := tx.ExecContext(ctx, `UPDATE page_grants SET retired_at=? WHERE page_id=? AND subject_type=? AND subject_id=? AND level=? AND granted_by_user_id=? AND granted_at=? AND retired_at IS NULL`,
			g.retiredAt, g.page, g.subjectType, g.subject, g.level, g.issuer, g.grantedAt); err != nil {
			return err
		}
	}
	if len(dump.Tables["workspace_members"]) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0)`)
	return err
}
