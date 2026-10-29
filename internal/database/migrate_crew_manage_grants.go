package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// SQLite cannot widen the shipped kind CHECK in place. No table references
// access_grants itself: rebuild this child table in the wrapper transaction,
// retaining dependent schema verbatim and leaving foreign_keys enabled.
func migrationCrewManageGrants(ctx context.Context, tx *sql.Tx, _ *slog.Logger) error {
	var dependents int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f ON f."table" COLLATE NOCASE='access_grants' WHERE m.type='table'`).Scan(&dependents); err != nil {
		return err
	}
	if dependents != 0 {
		return fmt.Errorf("crew Manage grants: unexpected incoming foreign keys")
	}
	type object struct{ kind, name, ddl string }
	var objects []object
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema WHERE (type IN ('view','trigger') OR (type='index' AND tbl_name='access_grants')) AND sql IS NOT NULL ORDER BY type DESC,name`)
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
		if o.kind == "view" && o.name == "live_access_grants" {
			found = true
			o.ddl = crewManageLiveGrantView
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
		return fmt.Errorf("crew Manage grants: missing live authority view")
	}
	// Refuse an unknown extension rather than silently discard its data.
	rows, err = tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('access_grants') ORDER BY cid`)
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
	if strings.Join(columns, ",") != "id,member_id,resource_kind,agent_id,project_id,routine_id,operation,created_by,created_at,crew_id" {
		return fmt.Errorf("crew Manage grants: unexpected grant schema")
	}
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	// All views/triggers are retained because SQLite reparses the entire schema
	// on rename, including transitive dependencies of live_access_grants.
	for _, kind := range []string{"trigger", "view"} {
		for _, o := range objects {
			if o.kind == kind {
				if _, err = tx.ExecContext(ctx, "DROP "+kind+" "+quote(o.name)); err != nil {
					return err
				}
			}
		}
	}
	if _, err = tx.ExecContext(ctx, crewManageGrantTable); err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, o.ddl); err != nil {
			return fmt.Errorf("restore %s %s: %w", o.kind, o.name, err)
		}
	}
	return nil
}

const crewManageGrantTable = `
CREATE TABLE access_grants_next (
 id TEXT PRIMARY KEY,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 resource_kind TEXT NOT NULL CHECK(resource_kind IN ('agent','project','routine','crew')),
 agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
 project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
 routine_id TEXT REFERENCES pipelines(id) ON DELETE CASCADE,
 operation TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL,
 crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
 CHECK((resource_kind='agent' AND agent_id IS NOT NULL AND project_id IS NULL AND routine_id IS NULL AND crew_id IS NULL)
 OR (resource_kind='project' AND project_id IS NOT NULL AND agent_id IS NULL AND routine_id IS NULL AND crew_id IS NULL)
 OR (resource_kind='routine' AND routine_id IS NOT NULL AND agent_id IS NULL AND project_id IS NULL AND crew_id IS NULL)
 OR (resource_kind='crew' AND crew_id IS NOT NULL AND agent_id IS NULL AND project_id IS NULL AND routine_id IS NULL)),
 CHECK(resource_kind<>'crew' OR operation IN ('view','use','manage')),
 UNIQUE(member_id,agent_id,operation), UNIQUE(member_id,project_id,operation),
 UNIQUE(member_id,routine_id,operation), UNIQUE(member_id,crew_id,operation)
);
INSERT INTO access_grants_next(id,member_id,resource_kind,agent_id,project_id,routine_id,operation,created_by,created_at,crew_id)
 SELECT id,member_id,resource_kind,agent_id,project_id,routine_id,operation,created_by,created_at,crew_id FROM access_grants;
DROP TABLE access_grants;
ALTER TABLE access_grants_next RENAME TO access_grants;
`

const crewManageLiveGrantView = `CREATE VIEW live_access_grants AS
WITH live AS NOT MATERIALIZED (
 SELECT g.* FROM access_grants g
 JOIN workspace_members recipient ON recipient.id=g.member_id
 JOIN workspaces w ON w.id=recipient.workspace_id AND w.deleted_at IS NULL
 JOIN workspace_members issuer ON issuer.user_id=g.created_by AND issuer.workspace_id=recipient.workspace_id
 JOIN users actor ON actor.id=issuer.user_id
 WHERE issuer.role IN ('OWNER','ADMIN') AND issuer.access_mode='trusted' AND actor.suspended_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=actor.id AND ceiling.access_mode='restricted')
 AND (g.resource_kind<>'crew' OR g.operation<>'manage' OR (recipient.access_mode='trusted'
   AND recipient.role IN ('MEMBER','MANAGER')
   AND EXISTS(SELECT 1 FROM users u WHERE u.id=recipient.user_id AND u.suspended_at IS NULL)
   AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=recipient.user_id AND ceiling.access_mode='restricted')))
 AND (g.resource_kind<>'crew' OR EXISTS(SELECT 1 FROM crews c WHERE c.id=g.crew_id AND c.workspace_id=recipient.workspace_id AND c.deleted_at IS NULL))
)
SELECT g.* FROM live g
UNION ALL
SELECT g.id,g.member_id,'agent',a.id,NULL,NULL,op.operation,g.created_by,g.created_at,NULL
 FROM live g JOIN crews c ON c.id=g.crew_id AND c.deleted_at IS NULL
 JOIN agents a ON a.crew_id=c.id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 JOIN (SELECT 'discover' AS operation UNION ALL SELECT 'chat' UNION ALL SELECT 'run') op
 ON (g.operation='view' AND op.operation='discover') OR g.operation='use'
 WHERE g.resource_kind='crew'
`
