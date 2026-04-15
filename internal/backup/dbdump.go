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
// inserted after their parents so FK constraints hold.
//
// Out of scope for MVP:
//   - users, workspace_members: membership is per-instance; restore
//     does not re-hydrate external user identities.
//   - credentials, oauth_tokens: handled separately by instance backup
//     (PR 4) and intentionally excluded from workspace bundles.
//   - sessions, audit_logs: operational data that stays with the
//     destination instance.
//
// Tables that may not exist in every schema revision are skipped at
// runtime; the exporter logs the skip so operators can see it.
var BackupTables = []string{
	"workspaces",
	"crews",
	"agents",
	"skills",
	"crew_members",
	"crew_integrations",
	"mcp_bindings",
	"agent_chats",
	"memory_backups",
	"workspace_memory",
	"crew_memory",
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
	case "agents":
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
	case "agent_chats":
		return "agent_id IN (SELECT a.id FROM agents a JOIN crews c ON a.crew_id = c.id WHERE c.workspace_id = ?)", []any{workspaceID}, true
	case "memory_backups":
		return "agent_id IN (SELECT a.id FROM agents a JOIN crews c ON a.crew_id = c.id WHERE c.workspace_id = ?)", []any{workspaceID}, true
	case "crew_members":
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
	case "crew_integrations":
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
	case "mcp_bindings":
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
	case "crew_memory":
		// crew_memory is crew-scoped — traverse via the crews FK so
		// DumpWorkspace does not silently miss rows the way the
		// generic "workspace_id = ?" branch would.
		return "crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", []any{workspaceID}, true
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
	for _, table := range BackupTables {
		exists, err := tableExists(ctx, tx, table)
		if err != nil {
			return nil, fmt.Errorf("backup: probe table %s: %w", table, err)
		}
		if !exists {
			continue
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
	}
	filters := []filter{
		{"workspaces", "id = ?", []any{workspaceID}},
		{"crews", "id = ?", []any{crewID}},
		{"agents", "crew_id = ?", []any{crewID}},
		{"skills", "workspace_id = ?", []any{workspaceID}},
		{"crew_members", "crew_id = ?", []any{crewID}},
		{"crew_integrations", "crew_id = ?", []any{crewID}},
		{"mcp_bindings", "crew_id = ?", []any{crewID}},
		{"agent_chats", "agent_id IN (SELECT id FROM agents WHERE crew_id = ?)", []any{crewID}},
		{"memory_backups", "agent_id IN (SELECT id FROM agents WHERE crew_id = ?)", []any{crewID}},
		{"crew_memory", "crew_id = ?", []any{crewID}},
	}
	for _, f := range filters {
		exists, err := tableExists(ctx, tx, f.table)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
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
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

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
	if err := preCommit(ctx); err != nil {
		return stats, err
	}
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("backup: commit restore tx: %w", err)
	}
	committed = true
	return stats, nil
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
