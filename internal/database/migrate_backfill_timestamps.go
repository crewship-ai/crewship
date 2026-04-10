package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// migrationBackfillLegacyTimestamps normalizes existing timestamp rows from
// SQLite's legacy `datetime('now')` format (`YYYY-MM-DD HH:MM:SS`) to RFC3339
// (`YYYY-MM-DDTHH:MM:SSZ`).
//
// Why this migration exists
// -------------------------
// The schema's CREATE TABLE statements use `DEFAULT (datetime('now'))` which
// produces the legacy space-separated format. Meanwhile, all Go-side writers
// (orchestrator, api handlers via `newUpdate`, scheduler, memory, seed code)
// format timestamps with `time.RFC3339`. A table can therefore end up with a
// mix of both formats in the same column, and because those formats are
// compared as text by SQLite, a `ORDER BY updated_at DESC` will sort them
// wrong: the space character (0x20) sorts BEFORE 'T' (0x54), so legacy rows
// always come after RFC3339 rows regardless of their actual time.
//
// This migration sweeps every user table's `*_at`-style TEXT columns and
// rewrites any row whose value is in the legacy format into RFC3339 via
// `replace(col, ' ', 'T') || 'Z'`. It's dynamic (discovers columns via
// `pragma_table_info`) so it keeps working as new tables are added, and
// idempotent (the LIKE filter only matches the exact 19-char legacy pattern),
// so running it twice is a no-op.
//
// What it does NOT do
// -------------------
//   - It does NOT change the column DEFAULTs, because that would require
//     table recreation for every affected table — sizeable scope, better
//     handled as a dedicated follow-up. Future rows written via DEFAULT will
//     still be legacy format; they'll need a later fix (either change the
//     DEFAULTs or ensure every INSERT passes an explicit RFC3339 value).
//   - It does NOT touch `_migrations.applied_at` or any `sqlite_*` tables.
//
// Safety
// ------
//   - Runs inside the migration's transaction. A failure rolls the whole
//     thing back cleanly.
//   - The LIKE pattern is exact-length (19 chars) and structurally restrictive
//     (`____-__-__ __:__:__`), so a non-timestamp TEXT column would only be
//     touched if its content happens to look exactly like a legacy timestamp.
//   - Identifiers from `sqlite_master`/`pragma_table_info` are validated as
//     safe ASCII identifiers before being interpolated into SQL, so the
//     runtime-generated UPDATE statements can't be coerced into injection.
func migrationBackfillLegacyTimestamps(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	tables, err := listUserTables(ctx, tx)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}

	var totalUpdated int64
	for _, table := range tables {
		cols, err := timestampColumns(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("describe %s: %w", table, err)
		}
		for _, col := range cols {
			// Both identifiers are validated by isSafeIdent below; interpolating
			// here is safe. Values still flow through parameterized binding.
			query := fmt.Sprintf(
				`UPDATE "%s" SET "%s" = replace("%s", ' ', 'T') || 'Z' `+
					`WHERE "%s" LIKE '____-__-__ __:__:__'`,
				table, col, col, col,
			)
			res, err := tx.ExecContext(ctx, query)
			if err != nil {
				return fmt.Errorf("backfill %s.%s: %w", table, col, err)
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				logger.Info("backfilled legacy timestamps",
					"table", table, "column", col, "rows", n)
				totalUpdated += n
			}
		}
	}
	logger.Info("timestamp backfill complete", "rows_updated", totalUpdated)
	return nil
}

// listUserTables returns every non-internal table name from sqlite_master.
// Excludes sqlite_*, _migrations, and anything else starting with underscore.
func listUserTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE '\_%' ESCAPE '\'
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !isSafeIdent(name) {
			// Should never happen for tables we create; defensive skip.
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// timestampColumns returns the subset of columns on the given table that look
// like timestamp fields: TEXT (or affinity-less) columns whose name ends in
// "_at" or is in a small set of explicit names. The narrow filter keeps the
// migration targeted — we don't want to rewrite random TEXT fields that happen
// to match the legacy date pattern, even though the LIKE filter already makes
// that extremely unlikely.
func timestampColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	if !isSafeIdent(table) {
		return nil, fmt.Errorf("unsafe table name: %q", table)
	}
	// pragma_table_info is the read-only alias; safe to parameterize via ?.
	rows, err := tx.QueryContext(ctx,
		`SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name, ctype string
		if err := rows.Scan(&name, &ctype); err != nil {
			return nil, err
		}
		if !isTimestampColumnName(name) {
			continue
		}
		if !isTextLikeType(ctype) {
			continue
		}
		if !isSafeIdent(name) {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// isTimestampColumnName decides whether a column name is a candidate for the
// backfill. We match on suffix `_at` (covers created_at, updated_at,
// started_at, completed_at, deleted_at, resolved_at, approved_at,
// last_used_at, last_checked_at, token_expires_at, …) plus a small
// allowlist of explicit names that don't follow the convention.
func isTimestampColumnName(name string) bool {
	if strings.HasSuffix(name, "_at") {
		return true
	}
	switch name {
	case "expires", "accepted_at", "cancel_at":
		// "expires" is the only non-_at timestamp column in the schema
		// (verification_tokens.expires); the others are defensive.
		return true
	}
	return false
}

// isTextLikeType returns true for SQLite column types that store text.
// SQLite is dynamically typed, so CREATE TABLE ... TEXT, VARCHAR(..),
// CHAR(..), CLOB, or even an empty type declaration all end up with TEXT
// affinity. We accept any of these.
func isTextLikeType(t string) bool {
	u := strings.ToUpper(strings.TrimSpace(t))
	if u == "" {
		return true // no declared type -> TEXT affinity by heuristic
	}
	if strings.Contains(u, "TEXT") || strings.Contains(u, "CHAR") || strings.Contains(u, "CLOB") {
		return true
	}
	return false
}

// isSafeIdent returns true if s is a conservative ASCII identifier safe to
// interpolate into a SQL statement. Only lowercase/uppercase letters,
// digits, and underscores are allowed; the first character must not be a
// digit. This is narrower than SQLite's actual identifier grammar on
// purpose — we don't need anything exotic, and refusing weird names keeps
// the dynamic SQL path robust even if sqlite_master ever returns something
// unexpected.
func isSafeIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
