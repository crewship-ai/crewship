package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// datetimeNowDefaultLiteral is the second-precision legacy DEFAULT expression
// this migration replaces: DEFAULT (datetime('now')) writes
// "YYYY-MM-DD HH:MM:SS" (space-form, no fraction). Every occurrence in the
// schema was written with this precise substring (no whitespace variation).
const datetimeNowDefaultLiteral = "datetime('now')"

// datetimeNowSubsecDefaultLiteral is the millisecond-fraction sibling:
// DEFAULT (datetime('now','subsec')) writes "YYYY-MM-DD HH:MM:SS.SSS" — still
// SPACE-form, so it sorts before any T-form value exactly like the plain form
// (' ' 0x20 < 'T' 0x54). ~16 ordered tables (pipelines, inbox_items,
// pending_runs, …) use it. The original #1073b pass matched only the plain
// literal and silently skipped every one of these, because "datetime('now')"
// is NOT a substring of "datetime('now','subsec')" (the char after 'now' is a
// comma, not the closing paren) — so this migration must match and rewrite
// both forms or the ordering bug it exists to close survives on those tables.
// Every schema occurrence uses this exact spelling (no whitespace variation,
// verified by repo-wide grep).
const datetimeNowSubsecDefaultLiteral = "datetime('now','subsec')"

// tformDefaultLiteral is the ISO T-form replacement: fixed-width
// millisecond-fraction, always UTC/'Z'. This is the SAME expression already
// used by every DEFAULT this codebase has added since the mixed-format bug
// was first understood (see migrate_consts_v111_conversation_search.go,
// v116, v118, v130, v137, and the inline DEFAULTs in migrate.go). Using
// anything else here would introduce a THIRD shape into columns that
// already mix the other two.
const tformDefaultLiteral = "strftime('%Y-%m-%dT%H:%M:%fZ','now')"

// datetimeNowDefaultSkipTables lists tables whose only `datetime('now')`
// DEFAULT column is intentionally left in legacy space-form — see the
// per-column verdict table in the #1073b PR description. Each entry has
// exactly one such column and it is confirmed (by repo-wide grep) never to
// appear in an ORDER BY, range comparison, or keyset-cursor bound:
//
//   - mcp_registry_servers.synced_at — write-only cache-refresh bookkeeping
//     (internal/api/mcp_registry.go upserts it via `excluded.synced_at`);
//     never read back, ordered, or compared.
//   - backup_locks.acquired_at — advisory-lock audit column, only ever
//     read back for display (internal/api/backup_query.go) or matched by
//     EXACT equality in the lock-release compare-and-delete
//     (internal/backup/lock.go: `AND acquired_at = ?`), never ordered or
//     range-compared.
//   - instance_config.installed_at — single-row (id=1) config table;
//     the column is never read back anywhere in the Go code.
//
// memory_versions is excluded for a different reason: it is 1073a's
// territory (PR #1172, migration v141). That slice already normalizes
// memory_versions.written_at via the tsformat writers/backfill approach;
// this migration must not re-touch it to avoid the two slices colliding on
// the same table.
var datetimeNowDefaultSkipTables = map[string]bool{
	"memory_versions":      true,
	"mcp_registry_servers": true,
	"backup_locks":         true,
	"instance_config":      true,
}

// migrationConvertDatetimeNowDefaults (v144) is the broader audit follow-up
// to #1073 (slice b of 3; 1073a/PR #1172 covered memory_versions
// specifically at v141). It converts every `DEFAULT (datetime('now'))` and
// `DEFAULT (datetime('now','subsec'))` column that is string-compared — appears in an ORDER BY / range /
// keyset-cursor comparison, or is a plausible pagination/sort key such as
// created_at/updated_at — to the ISO T-form DEFAULT already used elsewhere
// in this schema. The full per-column verdict (convert vs. intentionally
// left alone) is recorded in the PR description, not here, to keep this
// file focused on the mechanism.
//
// # Mechanism
//
// SQLite has no `ALTER TABLE … ALTER COLUMN … SET DEFAULT`. The
// documented alternative — https://www.sqlite.org/lang_altertable.html,
// "Making Other Kinds Of Table Schema Changes" — is the table-recreate
// dance (create a shadow table, copy rows, drop the original, rename the
// shadow into place). That was this migration's first implementation, and
// it does not survive contact with this schema's foreign key web: renaming
// or dropping a table that other tables reference by foreign key, with
// `PRAGMA foreign_keys` ON, makes SQLite (a) auto-rewrite every OTHER
// table's stored FOREIGN KEY clause to follow the rename — so a table
// renamed twice (out of the way, then a replacement renamed into place)
// leaves child tables' FK clauses pointing at whichever temp name existed
// when they last got rewritten, not the final name; and (b) run an
// implicit DELETE of every row before a DROP, so it can evaluate ON DELETE
// actions on any child rows — which fails outright for any parent table
// with a plain (no ON DELETE CASCADE/SET NULL) child reference, even
// though no row is actually being removed. `PRAGMA foreign_keys = OFF`
// would sidestep both, but that pragma is a documented no-op once a
// transaction is already open (confirmed empirically against this
// migration's own tx), and every migration here runs inside one — so there
// is no point in this function where foreign key enforcement can actually
// be turned off.
//
// This migration instead edits the DEFAULT clause's TEXT in place, via the
// `PRAGMA writable_schema` mechanism SQLite documents for exactly this
// class of "the table's SQL text needs a small, mechanical edit" case
// (https://www.sqlite.org/pragma.html#pragma_writable_schema — "an
// application to make schema changes that are not otherwise possible, or
// to fix a corrupt schema file"): rewrite the DEFAULT literal inside
// sqlite_master.sql for the affected table, then bump `schema_version` so
// every connection (including the current one, mid-transaction) reparses
// the schema instead of continuing to use a cached copy of the old one —
// confirmed necessary and sufficient by this migration's tests; without
// the bump, an already-open connection keeps compiling INSERTs against the
// stale, cached DEFAULT even after the sqlite_master row itself has
// changed.
//
// No table is dropped, renamed, or recreated: the table's rootpage,
// indexes, triggers, and foreign key definitions are completely untouched,
// so none of the cascading-rewrite or implicit-delete behavior above can
// be triggered. `PRAGMA foreign_key_check` still runs once at the end as a
// final integrity gate, even though nothing here should be able to trip
// it.
//
// Two concerns, both handled per converted table:
//
//  1. Schema text — rewrite the DEFAULT clause so NEW inserts stop
//     producing legacy-form values (the writable_schema dance above).
//  2. Existing data — normalize legacy-form rows the old DEFAULT already
//     wrote. Migration v45's one-shot backfill ran long before this
//     migration, so every insert relying on the DEFAULT between v45 and
//     here re-accumulated legacy-form `created_at`/`updated_at` values;
//     those still sort ahead of the T-form the fixed DEFAULT now produces
//     (' ' 0x20 < 'T' 0x54), which is the exact #1073b symptom. Fixing
//     only the DEFAULT would leave that historical pool broken forever, so
//     each converted table also gets v45's idempotent legacy→T-form sweep
//     (backfillLegacyTimestampRows) in the same pass. Scoped to converted
//     tables only, so the intentionally-skipped tables' data is never
//     touched.
func migrationConvertDatetimeNowDefaults(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	tables, err := listUserTables(ctx, tx)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}

	var converted []string
	var totalBackfilled int64
	for _, table := range tables {
		if datetimeNowDefaultSkipTables[table] {
			continue
		}

		createSQL, err := tableCreateSQL(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("read schema for %s: %w", table, err)
		}
		if createSQL == "" ||
			(!strings.Contains(createSQL, datetimeNowDefaultLiteral) &&
				!strings.Contains(createSQL, datetimeNowSubsecDefaultLiteral)) {
			continue
		}

		if err := rewriteTableDefaultLiteral(ctx, tx, table); err != nil {
			return fmt.Errorf("convert %s: %w", table, err)
		}
		// Normalize legacy-form rows this DEFAULT already wrote (see the
		// "Existing data" note above) — must run AFTER the schema rewrite
		// so a re-run finds nothing new to convert.
		n, err := backfillLegacyTimestampRows(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", table, err)
		}
		totalBackfilled += n
		converted = append(converted, table)
		logger.Info("converted datetime('now') DEFAULT to T-form",
			"table", table, "rows_backfilled", n)
	}

	if err := checkForeignKeys(ctx, tx); err != nil {
		return fmt.Errorf("post-conversion foreign_key_check: %w", err)
	}

	logger.Info("datetime('now') DEFAULT audit complete",
		"converted_tables", converted, "converted_count", len(converted),
		"rows_backfilled", totalBackfilled,
		"skipped_tables", skippedDatetimeNowDefaultTables())
	return nil
}

// backfillLegacyTimestampRows rewrites every legacy space-form value
// ("YYYY-MM-DD HH:MM:SS") in table's timestamp columns to ISO T-form
// ("...T...Z"), using the SAME idempotent expression migration v45 applies
// globally: replace(' ','T')||'Z', gated on the exact 19-char legacy
// pattern so a value already in T-form (or any non-legacy text) is left
// untouched and re-runs are no-ops. Column discovery reuses
// timestampColumns (the *_at-suffix + allowlist filter v45 uses), so every
// column a converted table's datetime('now') DEFAULT could have written is
// covered. Scoped to a single table so v144 only normalizes the tables
// whose DEFAULT it just converted, never the intentionally-skipped ones.
func backfillLegacyTimestampRows(ctx context.Context, tx *sql.Tx, table string) (int64, error) {
	cols, err := timestampColumns(ctx, tx, table)
	if err != nil {
		return 0, fmt.Errorf("describe %s: %w", table, err)
	}
	var total int64
	for _, col := range cols {
		// table and col are both validated by isSafeIdent inside
		// timestampColumns before they reach this interpolation.
		//
		// Fraction-aware gate (#1179): match BOTH the second-precision legacy
		// form ("YYYY-MM-DD HH:MM:SS", 19 chars, from datetime('now')) AND the
		// subsecond form ("YYYY-MM-DD HH:MM:SS.SSS", from datetime('now',
		// 'subsec')). A LIKE pattern of fixed underscores matches only its own
		// length, so the plain 19-char pattern never catches a fractional row —
		// leaving every subsec table's historical pool space-form and still
		// mis-ordered. Both patterns require a literal SPACE at the date/time
		// boundary, which T-form ('T' there) never has, so already-converted
		// values match neither and re-runs stay no-ops. replace(' ','T')||'Z'
		// transforms both correctly (one space, fraction preserved untouched).
		query := fmt.Sprintf(
			`UPDATE "%s" SET "%s" = replace("%s", ' ', 'T') || 'Z' `+
				`WHERE "%s" LIKE '____-__-__ __:__:__' `+
				`OR "%s" LIKE '____-__-__ __:__:__.%%'`,
			table, col, col, col, col,
		)
		res, err := tx.ExecContext(ctx, query)
		if err != nil {
			return total, fmt.Errorf("backfill %s.%s: %w", table, col, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func skippedDatetimeNowDefaultTables() []string {
	names := make([]string, 0, len(datetimeNowDefaultSkipTables))
	for t := range datetimeNowDefaultSkipTables {
		names = append(names, t)
	}
	return names
}

// tableCreateSQL returns the live CREATE TABLE statement sqlite_master has
// on file for table, or "" if the table doesn't exist (defensive — every
// name here comes from listUserTables against the same connection/tx, so
// this should never actually miss).
func tableCreateSQL(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	var sqlText sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sqlText.String, nil
}

// rewriteTableDefaultLiteral replaces every occurrence of
// datetimeNowDefaultLiteral in table's stored CREATE TABLE sql with
// tformDefaultLiteral, using PRAGMA writable_schema, then bumps
// schema_version so the rewrite takes effect immediately — including for
// statements later in this SAME transaction, and for other connections
// pulled from the pool after commit. Without the version bump, SQLite's
// already-parsed in-memory schema for open connections keeps compiling new
// statements (e.g. a subsequent INSERT relying on the DEFAULT) against the
// OLD text, even though sqlite_master's row has already changed on disk —
// this was confirmed empirically while building this migration and is not
// something PRAGMA writable_schema does automatically.
func rewriteTableDefaultLiteral(ctx context.Context, tx *sql.Tx, table string) error {
	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = ON`); err != nil {
		return fmt.Errorf("enable writable_schema: %w", err)
	}
	// writable_schema is connection-level state, not transactional, so it
	// must be turned back off unconditionally — even on the error paths
	// below — or it leaks into whatever runs next on this connection.
	defer func() { _, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`) }()

	// Rewrite BOTH legacy forms to the same fractional T-form in one pass.
	// The two literals are disjoint (neither is a substring of the other, nor
	// of the strftime replacement), so the nested replace is order-independent
	// and cannot corrupt a table that mixes both forms across columns.
	res, err := tx.ExecContext(ctx,
		`UPDATE sqlite_master SET sql = replace(replace(sql, ?, ?), ?, ?) WHERE type = 'table' AND name = ?`,
		datetimeNowSubsecDefaultLiteral, tformDefaultLiteral,
		datetimeNowDefaultLiteral, tformDefaultLiteral,
		table,
	)
	if err != nil {
		return fmt.Errorf("rewrite sqlite_master.sql for %s: %w", table, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("check rows affected for %s: %w", table, err)
	} else if n != 1 {
		return fmt.Errorf("expected to rewrite exactly 1 sqlite_master row for %s, rewrote %d", table, n)
	}

	// PRAGMA schema_version = N is itself the way SQLite documents forcing
	// a reparse (see pragma.html#pragma_schema_version): setting it to any
	// value other than the value just read invalidates every connection's
	// cached schema, so the very next statement on any connection —
	// including this one, later in this same transaction — reads the
	// rewritten sqlite_master row instead of a stale in-memory copy.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
		return fmt.Errorf("bump schema_version after rewriting %s: %w", table, err)
	}
	return nil
}

// checkForeignKeys runs PRAGMA foreign_key_check and returns an error
// describing every violation found, if any. Run once after every table's
// DEFAULT has been rewritten, as a final integrity gate before the
// migration's transaction commits. Nothing in this migration should be
// able to trip it (no table is dropped, renamed, or has its data touched),
// but it costs little to confirm rather than assume.
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var violations []string
	for rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		violations = append(violations, fmt.Sprintf("%s (rowid=%v) -> %s (fkid=%d)", table, rowid, parent, fkid))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign key violations after DEFAULT rewrite: %s", strings.Join(violations, "; "))
	}
	return nil
}
