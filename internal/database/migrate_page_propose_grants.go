package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// SQLite cannot widen the shipped grant-level CHECK in place. Rebuild in the
// normal migration transaction with FK enforcement on. Discover and restore
// the current schema, including owner-only retirement/audit triggers shipped
// after the original Propose branch, instead of replaying its stale snapshot.
func migrationPageProposeGrants(ctx context.Context, tx *sql.Tx, _ *slog.Logger) error {
	var dependents int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f ON f."table" COLLATE NOCASE='page_grants' WHERE m.type='table'`).Scan(&dependents); err != nil {
		return err
	}
	if dependents != 0 {
		return fmt.Errorf("Page Propose grants: unexpected incoming foreign keys")
	}
	type object struct{ kind, name, ddl string }
	var objects []object
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema WHERE (type IN ('view','trigger') OR (type='index' AND tbl_name='page_grants')) AND sql IS NOT NULL ORDER BY type DESC,name`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.kind, &o.name, &o.ddl); err != nil {
			rows.Close()
			return err
		}
		if o.kind == "view" && o.name == "page_grant_verdicts" {
			found = true
		}
		objects = append(objects, o)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("Page Propose grants: missing live authority view")
	}
	// Refuse an unknown extension rather than silently discard its data.
	rows, err = tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('page_grants') ORDER BY cid`)
	if err != nil {
		return err
	}
	var columns []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, name)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if strings.Join(columns, ",") != "page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at,retired_at" {
		return fmt.Errorf("Page Propose grants: unexpected grant schema")
	}
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	// All views/triggers are retained because SQLite reparses the entire schema
	// on rename, including transitive dependencies of page_grant_verdicts.
	for _, kind := range []string{"trigger", "view"} {
		for _, o := range objects {
			if o.kind == kind {
				if _, err = tx.ExecContext(ctx, "DROP "+kind+" "+quote(o.name)); err != nil {
					return err
				}
			}
		}
	}
	if _, err = tx.ExecContext(ctx, pageProposeGrantTable); err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, o.ddl); err != nil {
			return fmt.Errorf("restore %s %s: %w", o.kind, o.name, err)
		}
	}
	return nil
}

const pageProposeGrantTable = `
CREATE TABLE page_grants_propose (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 subject_type TEXT NOT NULL CHECK(subject_type IN ('user','crew','agent','workspace')),
 subject_id TEXT NOT NULL DEFAULT '',
 level TEXT NOT NULL CHECK(level IN ('read','comment','propose','produce','write')),
 panel_ids TEXT,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 granted_at TEXT NOT NULL DEFAULT (datetime('now')),
 retired_at TEXT,
 PRIMARY KEY(page_id,subject_type,subject_id,level),
 CHECK(panel_ids IS NULL OR level='produce'),
 CHECK(subject_type<>'workspace' OR (subject_id='' AND level IN ('read','comment','write'))),
 CHECK(subject_type<>'agent' OR level<>'comment'),
 CHECK(level<>'propose' OR subject_type='agent')
);
INSERT INTO page_grants_propose(rowid,page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at,retired_at)
 SELECT rowid,page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at,retired_at FROM page_grants;
DROP TABLE page_grants;
ALTER TABLE page_grants_propose RENAME TO page_grants;
`
