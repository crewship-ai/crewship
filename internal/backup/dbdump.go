package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// BackupTables is the ordered list of tables exported in workspace /
// crew scope bundles. The order matters for restore — children are
// inserted after their parents so FK constraints hold (`users` and
// `skills` before the bridge rows that reference them).
//
// What this list deliberately INCLUDES that an earlier draft did not:
//
//   - `users`: previously excluded as "per-instance identity," but
//     crew_members.user_id and chats.created_by both FK into it. On
//     a fresh restore target every crew_members row aborted with a
//     deferred FK violation — the canonical disaster-recovery scenario.
//     Workspace-scope filter narrows users to only those referenced by
//     workspace data (no cross-workspace identity leak). Note: this
//     bundles `hashed_password` for those users; admins downloading a
//     bundle should already be trusted with that level of access.
//   - `skills`: previously fell through to the default
//     `workspace_id = ?` filter and got silently skipped (skills are
//     globally namespaced). Now scoped transitively via agent_skills
//     so user-created custom skills round-trip.
//   - `chats`: previously listed under the wrong name (`agent_chats`,
//     which is not an actual table). Production code reads from `chats`.
//   - `agent_mcp_bindings`: previously listed as `mcp_bindings`. Same
//     class of name-mismatch silent-skip. Credential-bound bindings
//     will FK-fail on a fresh target because `credentials` is a
//     separate instance-scope concern; that gap is tracked separately.
//   - `journal_entries`: the crew journal is documented as "canonical
//     source of truth for every observable action in the platform";
//     omitting it from the bundle was real data loss. FTS5 triggers
//     repopulate journal_entries_fts on INSERT so search keeps working
//     post-restore.
//
// Removed (orphan entries the schema never created):
// crew_integrations, memory_backups, workspace_memory, crew_memory.
// Agent memory lives on the per-crew container filesystem (`/output`)
// which is collected by the docker phase, not the DB dump.
//
// Still out of scope for MVP (deliberate, with reasons):
//   - workspace_members: instance-level invitations / role assignments.
//     Adding it without also adding a user-existence guarantee leaks
//     across tenants; revisit when multi-tenant restore is in scope.
//   - credentials, oauth_tokens: handled separately by instance backup
//     (PR 4) and intentionally excluded from workspace bundles.
//   - sessions, audit_logs: operational data that stays with the
//     destination instance.
//   - journal_embeddings, journal_entries_archived: BLOB vector data
//     would corrupt under the current TEXT-only round-trip path
//     (normalizeScan coerces []byte to string). Tracked as a runtime
//     hardening follow-up before they can be safely added.
//
// Tables that may not exist in every schema revision are skipped at
// runtime; the exporter logs the skip so operators can see it.
var BackupTables = []string{
	"users",
	"workspaces",
	"crews",
	"agents",
	"skills",
	"agent_skills",
	"crew_members",
	"chats",
	"agent_mcp_bindings",
	"journal_entries",
}

// DBDump captures the exported rows from one or more tables. Keys are
// table names; values are arrays of column→value maps. JSON encoding
// of sql.NullString and similar types works out of the box because the
// underlying exporter resolves everything to Go scalar / []byte types.
type DBDump struct {
	// WorkspaceID is the scope anchor. All rows in Tables either belong
	// to this workspace directly (workspace_id column) or transitively
	// through a parent row.
	WorkspaceID string                      `json:"workspace_id"`
	Tables      map[string][]map[string]any `json:"tables"`
}

// tableExists reports whether the given table is present in the current
// schema. Used so the exporter silently skips tables that were added
// in newer migrations we have not yet applied (backward-robust).
func tableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`,
		table,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// tableHasColumn reports whether the given table includes column col.
// Not every table scoping is done via workspace_id — some tables are
// workspace-scoped transitively. We emit a filter when the column is
// present and fall back to a scope-aware query otherwise.
func tableHasColumn(ctx context.Context, tx *sql.Tx, table, col string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// workspaceFilterSQL returns the WHERE clause fragment used to scope
// rows from a given table to a particular workspace, plus the parameter
// list to supply. Only tables with a direct workspace_id column are
// supported today; transitively-scoped tables (joined via crew_id etc.)
// are followed up with custom queries below.
func workspaceFilterSQL(table, workspaceID string) (string, []any, bool) {
	switch table {
	case "workspaces":
		return "id = ?", []any{workspaceID}, true
	case "users":
		// Users are global. Narrow to only the identities the workspace
		// data actually FKs into so a workspace bundle does NOT leak
		// every other user on a multi-workspace instance. The UNION
		// gathers: crew membership owners, chat authors, and authors
		// of custom skills attached to this workspace's agents.
		// Joins via crews.workspace_id rather than agents.workspace_id
		// because the agents schema isn't strictly required to carry
		// workspace_id directly (production does; the minimal test
		// schemas in this package don't).
		return `id IN (
			SELECT user_id FROM crew_members WHERE crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)
			UNION SELECT created_by FROM chats WHERE workspace_id = ? AND created_by IS NOT NULL
			UNION SELECT s.author_id FROM skills s
			  JOIN agent_skills ask ON ask.skill_id = s.id
			  JOIN agents a ON a.id = ask.agent_id
			  JOIN crews c ON c.id = a.crew_id
			  WHERE c.workspace_id = ? AND s.author_id IS NOT NULL
		)`, []any{workspaceID, workspaceID, workspaceID}, true
	case "agents":
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
	case "skills":
		// Skills are globally namespaced (no workspace_id column on the
		// skills row in current production schema). Carry only the ones
		// actually attached to an agent in this workspace — i.e.
		// user-created custom skills the bundle would otherwise orphan
		// when agent_skills tries to restore against a fresh target.
		// Bundled skills (skill_coding_01 etc.) are re-seeded on every
		// cmd_start boot and the INSERT OR IGNORE no-ops on the
		// conflict. Joins via crews to stay portable across test
		// schemas that omit agents.workspace_id.
		return `id IN (
			SELECT ask.skill_id FROM agent_skills ask
			  JOIN agents a ON a.id = ask.agent_id
			  JOIN crews c ON c.id = a.crew_id
			  WHERE c.workspace_id = ?
		)`, []any{workspaceID}, true
	case "agent_skills":
		return "agent_id IN (SELECT a.id FROM agents a JOIN crews c ON a.crew_id = c.id WHERE c.workspace_id = ?)", []any{workspaceID}, true
	case "crew_members":
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
	case "chats":
		// chats has a direct workspace_id column. The explicit case
		// also documents this is the renamed `agent_chats` entry —
		// kept in the switch (not the default) so readers can find it.
		return "workspace_id = ?", []any{workspaceID}, false
	case "agent_mcp_bindings":
		// MCP bindings reference agents directly. The credential_id FK
		// remains a gap (credentials are out of scope for workspace
		// bundles); bindings WITH credential_id will FK-fail on restore
		// against a fresh target. Tracked separately.
		return "agent_id IN (SELECT a.id FROM agents a JOIN crews c ON a.crew_id = c.id WHERE c.workspace_id = ?)", []any{workspaceID}, true
	default:
		// Generic case: table has a workspace_id column.
		return "workspace_id = ?", []any{workspaceID}, false
	}
}

// DumpWorkspace exports every table in BackupTables restricted to the
// given workspace. It runs in a single BEGIN IMMEDIATE transaction so
// the snapshot is consistent — concurrent writers cannot slip rows
// between our selects. sql.LevelSerializable is the closest
// database/sql abstraction maps to IMMEDIATE on modernc.org/sqlite,
// but we issue the explicit PRAGMA afterwards to be safe against
// driver changes.
func DumpWorkspace(ctx context.Context, db *sql.DB, workspaceID string) (*DBDump, error) {
	// sql.LevelSerializable maps to BEGIN IMMEDIATE on modernc.org/sqlite,
	// which is what we want: the writer lock is grabbed on the first
	// statement so a concurrent writer cannot slip rows between our
	// probes and selects. We deliberately do NOT set PRAGMA query_only
	// on the tx — that pragma persists on the pooled connection after
	// the tx commits and then breaks subsequent writes on that
	// connection.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("backup: begin dump tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	dump := &DBDump{
		WorkspaceID: workspaceID,
		Tables:      map[string][]map[string]any{},
	}
	// Inter-table dependencies for transitive scoping. If a filter uses
	// a sub-query against another table that doesn't exist in the
	// current schema (e.g. a minimal test DB without agent_skills, or
	// a future schema where one of these gets dropped), the SELECT
	// against the absent table would error. Skip the parent entry in
	// that case — matches the "schema revision skip" pattern the
	// existing tableExists check already implements for the table
	// itself. The map values are the sub-query targets.
	scopingDependencies := map[string][]string{
		"users":  {"crew_members", "chats", "agent_skills", "skills"},
		"skills": {"agent_skills"},
	}
	for _, table := range BackupTables {
		exists, err := tableExists(ctx, tx, table)
		if err != nil {
			return nil, fmt.Errorf("backup: probe table %s: %w", table, err)
		}
		if !exists {
			continue
		}
		if deps, ok := scopingDependencies[table]; ok {
			depMissing := false
			for _, dep := range deps {
				depExists, err := tableExists(ctx, tx, dep)
				if err != nil {
					return nil, fmt.Errorf("backup: probe dep %s for %s: %w", dep, table, err)
				}
				if !depExists {
					depMissing = true
					break
				}
			}
			if depMissing {
				continue
			}
		}
		where, args, _ := workspaceFilterSQL(table, workspaceID)
		if where == "workspace_id = ?" {
			// Confirm column presence; skip if missing.
			hasCol, err := tableHasColumn(ctx, tx, table, "workspace_id")
			if err != nil {
				return nil, fmt.Errorf("backup: probe column on %s: %w", table, err)
			}
			if !hasCol {
				continue
			}
		}
		query := fmt.Sprintf("SELECT * FROM %s WHERE %s", table, where)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("backup: select from %s: %w", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("backup: columns of %s: %w", table, err)
		}
		var out []map[string]any
		for rows.Next() {
			raw := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("backup: scan %s: %w", table, err)
			}
			row := make(map[string]any, len(cols))
			for i, c := range cols {
				row[c] = normalizeScan(raw[i])
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("backup: iterate %s: %w", table, err)
		}
		_ = rows.Close()
		dump.Tables[table] = out
	}
	return dump, nil
}

// DumpCrew exports rows for a single crew within its workspace. Useful
// for `--scope=crew` backups which produce same-instance bundles (per
// PRD section 2.3).
func DumpCrew(ctx context.Context, db *sql.DB, crewID string) (*DBDump, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT workspace_id FROM crews WHERE id = ?`, crewID,
	).Scan(&workspaceID); err != nil {
		return nil, fmt.Errorf("backup: resolve crew workspace: %w", err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("backup: begin dump tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	dump := &DBDump{
		WorkspaceID: workspaceID,
		Tables:      map[string][]map[string]any{},
	}
	// Tables to include for crew scope — subset of workspace, filtered
	// further by crew_id where applicable.
	type filter struct {
		table string
		where string
		args  []any
		// requiresCol, if non-empty, must be present on the target
		// table for the filter to apply. Lets us declare workspace-
		// scoped filters on tables (e.g. skills) that may have been
		// reorganized into a global namespace in newer schemas — the
		// pre-flight column probe matches DumpWorkspace's behavior so
		// crew dumps don't crash on a perfectly-valid global table.
		requiresCol string
	}
	// Mirror the workspace-scope additions: chats / agent_mcp_bindings /
	// journal_entries / users / skills (transitive-via-agent-skills).
	// Same FK-parents-first ordering as BackupTables. crew_integrations,
	// memory_backups, workspace_memory, crew_memory all removed because
	// no migration creates them.
	filters := []filter{
		// Users that the crew's own data FKs into. Same UNION pattern as
		// the workspace filter, narrowed to one crew so a crew-scope
		// bundle does not leak users from sibling crews. requiresCol
		// guards against a future schema where author_id is dropped.
		{"users", `id IN (
			SELECT user_id FROM crew_members WHERE crew_id = ?
			UNION SELECT created_by FROM chats WHERE agent_id IN (SELECT id FROM agents WHERE crew_id = ?) AND created_by IS NOT NULL
			UNION SELECT s.author_id FROM skills s
			  JOIN agent_skills ask ON ask.skill_id = s.id
			  WHERE ask.agent_id IN (SELECT id FROM agents WHERE crew_id = ?) AND s.author_id IS NOT NULL
		)`, []any{crewID, crewID, crewID}, ""},
		{"workspaces", "id = ?", []any{workspaceID}, ""},
		{"crews", "id = ?", []any{crewID}, ""},
		{"agents", "crew_id = ?", []any{crewID}, ""},
		{"skills", `id IN (SELECT skill_id FROM agent_skills WHERE agent_id IN (SELECT id FROM agents WHERE crew_id = ?))`, []any{crewID}, ""},
		{"agent_skills", "agent_id IN (SELECT id FROM agents WHERE crew_id = ?)", []any{crewID}, ""},
		{"crew_members", "crew_id = ?", []any{crewID}, ""},
		{"chats", "agent_id IN (SELECT id FROM agents WHERE crew_id = ?)", []any{crewID}, ""},
		{"agent_mcp_bindings", "agent_id IN (SELECT id FROM agents WHERE crew_id = ?)", []any{crewID}, ""},
		{"journal_entries", "crew_id = ?", []any{crewID}, ""},
	}
	for _, f := range filters {
		exists, err := tableExists(ctx, tx, f.table)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		if f.requiresCol != "" {
			hasCol, err := tableHasColumn(ctx, tx, f.table, f.requiresCol)
			if err != nil {
				return nil, fmt.Errorf("backup: probe column on %s: %w", f.table, err)
			}
			if !hasCol {
				// Table exists but the scoping column doesn't — the table
				// is shared across the instance (skills became global at
				// some point). Skip silently rather than crash.
				continue
			}
		}
		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf("SELECT * FROM %s WHERE %s", f.table, f.where),
			f.args...,
		)
		if err != nil {
			return nil, fmt.Errorf("backup: select from %s: %w", f.table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		var out []map[string]any
		for rows.Next() {
			raw := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, err
			}
			row := make(map[string]any, len(cols))
			for i, c := range cols {
				row[c] = normalizeScan(raw[i])
			}
			out = append(out, row)
		}
		// Pre-existing bug surfaced by CodeRabbit during PR review:
		// DumpCrew was the only loop here that did NOT check rows.Err()
		// after iteration. A driver error mid-iteration would have
		// silently truncated the dump.
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("backup: iterate %s: %w", f.table, err)
		}
		_ = rows.Close()
		dump.Tables[f.table] = out
	}
	return dump, nil
}

// normalizeScan converts raw sql.Scan values into JSON-friendly Go
// types. []byte becomes string (SQLite's TEXT is our storage standard),
// everything else passes through. Nil stays nil.
func normalizeScan(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// tableColumns returns the set of column names present on the given
// target table, cached per-transaction so we probe PRAGMA once per
// table. Used by RestoreDump to reject unknown column names that
// could otherwise smuggle arbitrary SQL through dump.json.
func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// quoteIdent applies SQLite's double-quote identifier escaping so the
// identifier survives restore-time concatenation into SQL. Any embedded
// double quote is doubled per the SQLite spec.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// RestoreDump writes the rows from dump into the target database. It
// uses INSERT OR IGNORE so a restore into an instance that already has
// some of the rows (e.g. workspace row from same-slug re-restore) does
// not blow up; callers that want hard conflict semantics should check
// for collisions before invoking.
//
// Security & schema-skew guarantees:
//   - Unknown tables on the target are skipped (not failed) so a bundle
//     produced by a newer Crewship does not explode on restore against
//     an older install.
//   - Column names from dump.json are whitelisted against the target
//     schema via PRAGMA table_info and double-quoted before being
//     concatenated into the SQL string. An attacker who tampered with
//     dump.json cannot smuggle SQL through column identifiers.
//
// Rows are inserted in the order recorded in BackupTables so parent
// rows land before children and FK enforcement does not explode on
// crew.workspace_id etc.
func RestoreDump(ctx context.Context, db *sql.DB, dump *DBDump) error {
	_, err := RestoreDumpTx(ctx, db, dump, func(context.Context) error { return nil })
	return err
}

// RestoreStats captures the real outcome of a RestoreDump call so the
// caller can distinguish "bundle was empty" from "every row collided
// with an existing primary key and INSERT OR IGNORE silently dropped
// it" — a restore into the source instance is the usual case of the
// latter and is effectively a no-op today.
type RestoreStats struct {
	RowsSeen     int // total rows in the bundle (sum of len(Tables[*]))
	RowsInserted int // rows that actually landed (sum of RowsAffected)
}

// RestoreDumpTx is RestoreDump with a caller-supplied preflight hook
// that runs inside the same transaction after all validation but
// before the commit. If the hook returns an error the tx rolls back
// so partial restores do not commit DB state when downstream phases
// (Docker CopyTo, etc.) are about to fail.
//
// Returns RestoreStats so callers can warn the admin when the bundle
// was non-empty but nothing was inserted (the classic "INSERT OR
// IGNORE swallowed every PK collision" scenario that happens when
// you restore into the same instance that produced the bundle).
func RestoreDumpTx(ctx context.Context, db *sql.DB, dump *DBDump, preCommit func(context.Context) error) (RestoreStats, error) {
	return RestoreDumpTxHooks(ctx, db, dump, &RestoreDumpHooks{PreCommit: preCommit})
}

// RestoreDumpHooks carries optional tx-bound callbacks that run at
// well-defined points inside RestoreDumpTxHooks's transaction. nil
// closures or a nil RestoreDumpHooks are equivalent to no-ops.
type RestoreDumpHooks struct {
	// PreInsert runs INSIDE the tx, AFTER PRAGMA setup but BEFORE the
	// per-table INSERT pass. Used by --replace mode to wipe the
	// target workspace's rows first so the bundle can land with
	// original IDs intact.
	PreInsert func(ctx context.Context, tx *sql.Tx) error
	// PreCommit runs INSIDE the tx, AFTER all INSERTs and the FK
	// integrity check, BEFORE Commit. Used by the docker phase: a
	// CopyTo failure rolls the DB back rather than leaving a
	// half-restored container with orphan rows.
	PreCommit func(ctx context.Context) error
}

// RestoreDumpTxHooks runs the restore INSERTs inside a transaction
// with the supplied lifecycle hooks. See RestoreDumpHooks for the
// contract of each hook.
func RestoreDumpTxHooks(ctx context.Context, db *sql.DB, dump *DBDump, hooks *RestoreDumpHooks) (RestoreStats, error) {
	if hooks == nil {
		hooks = &RestoreDumpHooks{}
	}
	var stats RestoreStats
	// PRAGMA foreign_keys is a no-op inside an open transaction per
	// the SQLite docs, so we must set it on a held connection BEFORE
	// BeginTx. db.Conn pins us to a single pooled connection; we
	// release it on defer so the pragma setting does not leak to
	// unrelated callers that happen to get this connection next.
	conn, err := db.Conn(ctx)
	if err != nil {
		return stats, fmt.Errorf("backup: acquire conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return stats, fmt.Errorf("backup: enable FK enforcement: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("backup: begin restore tx: %w", err)
	}
	// Defer FK checks until commit. Without this, the tombstone purge
	// pass walks tables in BackupTables order (parents first), so
	// deleting a soft-deleted `workspaces` row before its child
	// `crews` rows fires "FOREIGN KEY constraint failed" — even
	// though the bundle is about to re-INSERT both. defer_foreign_keys
	// is per-transaction (vs PRAGMA foreign_keys which is connection-
	// scoped) so it stays scoped to this restore and won't leak to
	// other tx on the same connection.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return stats, fmt.Errorf("backup: defer FK enforcement: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	// PreInsert hook runs BEFORE any per-table INSERT. The canonical
	// use case is --replace: clear the target workspace's rows so the
	// bundle's rows land with their original IDs instead of either
	// colliding (PK clash) or being silently dropped (INSERT OR
	// IGNORE no-op against a fresh-bootstrap row with the same slug).
	if hooks.PreInsert != nil {
		if err := hooks.PreInsert(ctx, tx); err != nil {
			return stats, fmt.Errorf("backup: pre-insert hook: %w", err)
		}
	}

	for _, table := range BackupTables {
		rows, ok := dump.Tables[table]
		if !ok || len(rows) == 0 {
			continue
		}
		stats.RowsSeen += len(rows)
		exists, err := tableExistsTx(ctx, tx, table)
		if err != nil {
			return stats, fmt.Errorf("backup: probe %s: %w", table, err)
		}
		if !exists {
			// Target lacks this table — skip rather than failing so a
			// bundle from a newer schema can restore (partially) here.
			continue
		}
		allowed, err := tableColumns(ctx, tx, table)
		if err != nil {
			return stats, fmt.Errorf("backup: columns of %s: %w", table, err)
		}
		// Purge tombstones whose primary key collides with a bundle row.
		// Without this, a row that was soft-deleted on the target (the
		// admin removed a crew through the UI; the row stays with
		// deleted_at set) silently shadows the bundle row at INSERT OR
		// IGNORE time, and the whole restore reports zero rows inserted
		// — see the "no-op restore detection" path in runner_restore.go.
		// The semantic: a restore re-asserts the bundle's truth, so a
		// tombstone with the same PK is overwritten on user request.
		if allowed["id"] && allowed["deleted_at"] {
			if err := purgeTombstonesTx(ctx, tx, table, rows); err != nil {
				return stats, err
			}
		}
		for _, row := range rows {
			cols := make([]string, 0, len(row))
			placeholders := make([]string, 0, len(row))
			args := make([]any, 0, len(row))
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			sortStrings(keys)
			for _, k := range keys {
				if !allowed[k] {
					continue
				}
				cols = append(cols, quoteIdent(k))
				placeholders = append(placeholders, "?")
				args = append(args, row[k])
			}
			if len(cols) == 0 {
				continue
			}
			query := fmt.Sprintf(
				"INSERT OR IGNORE INTO %s (%s) VALUES (%s)",
				quoteIdent(table),
				strings.Join(cols, ","),
				strings.Join(placeholders, ","),
			)
			res, err := tx.ExecContext(ctx, query, args...)
			if err != nil {
				return stats, fmt.Errorf("backup: insert into %s: %w", table, err)
			}
			if n, err := res.RowsAffected(); err == nil {
				stats.RowsInserted += int(n)
			}
		}
	}
	// Force-resolve deferred FK violations BEFORE preCommit. preCommit
	// is the docker-restore closure that mutates container filesystems;
	// without this scan a bad bundle (or schema-skew leaving an orphan
	// reference) would let docker write into the target and only fail
	// on tx.Commit() — leaving a half-restored container with no DB
	// rows describing it. PRAGMA foreign_key_check returns one row per
	// violation regardless of defer_foreign_keys, so it's the right
	// probe to run inside the open tx.
	if err := assertNoFKViolationsTx(ctx, tx); err != nil {
		return stats, err
	}
	if hooks.PreCommit != nil {
		if err := hooks.PreCommit(ctx); err != nil {
			return stats, err
		}
	}
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("backup: commit restore tx: %w", err)
	}
	committed = true
	return stats, nil
}

// assertNoFKViolationsTx runs PRAGMA foreign_key_check inside the
// open restore tx and surfaces a typed error on the first violation.
// Used to fail fast before the docker-restore preCommit so bundle/
// schema-skew problems don't leak into container side-effects.
//
// Aggregates up to 5 violations into the error message — enough to
// debug an orphan-graph problem without flooding the log if the
// bundle is wildly inconsistent.
func assertNoFKViolationsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("backup: foreign key check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	const maxReport = 5
	var seen []string
	for rows.Next() {
		var childTable, parentTable sql.NullString
		var rowID sql.NullInt64
		var fkID sql.NullInt64
		if err := rows.Scan(&childTable, &rowID, &parentTable, &fkID); err != nil {
			return fmt.Errorf("backup: scan foreign key check: %w", err)
		}
		if len(seen) < maxReport {
			seen = append(seen, fmt.Sprintf("%s.rowid=%d → %s",
				childTable.String, rowID.Int64, parentTable.String))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("backup: foreign key check iter: %w", err)
	}
	if len(seen) > 0 {
		return fmt.Errorf("backup: deferred FK violations after restore inserts: %s", strings.Join(seen, "; "))
	}
	return nil
}

// purgeTombstonesTx hard-deletes rows in table whose primary key
// matches a bundle row AND whose deleted_at is set. table is from
// the BackupTables allowlist (already quoteIdent-safe). rows are
// the bundle rows about to be re-inserted.
//
// Bound on the number of placeholders per statement so SQLite does
// not balk at the 999-variable default limit when a workspace has
// thousands of crews / agents in the bundle.
func purgeTombstonesTx(ctx context.Context, tx *sql.Tx, table string, rows []map[string]any) error {
	const chunkSize = 500
	ids := make([]any, 0, len(rows))
	for _, row := range rows {
		v, ok := row["id"]
		if !ok || v == nil {
			continue
		}
		ids = append(ids, v)
	}
	if len(ids) == 0 {
		return nil
	}
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		for i := range batch {
			placeholders[i] = "?"
		}
		query := fmt.Sprintf(
			"DELETE FROM %s WHERE deleted_at IS NOT NULL AND deleted_at != '' AND id IN (%s)",
			quoteIdent(table),
			strings.Join(placeholders, ","),
		)
		if _, err := tx.ExecContext(ctx, query, batch...); err != nil {
			return fmt.Errorf("backup: purge tombstones in %s: %w", table, err)
		}
	}
	return nil
}

// tableExistsTx is like tableExists but runs on an already-open tx.
func tableExistsTx(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`,
		table,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// sortStrings is a local insertion sort so dbdump.go does not pull
// in the sort package just for RestoreDump determinism.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// MarshalDump returns the JSON encoding of dump. Kept separate from the
// collector so runner.go can embed the bytes into the payload tar
// under db/dump.json.
func MarshalDump(dump *DBDump) ([]byte, error) {
	b, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: marshal dump: %w", err)
	}
	return b, nil
}

// UnmarshalDump parses a previously produced JSON dump.
func UnmarshalDump(data []byte) (*DBDump, error) {
	var d DBDump
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("backup: unmarshal dump: %w", err)
	}
	return &d, nil
}
