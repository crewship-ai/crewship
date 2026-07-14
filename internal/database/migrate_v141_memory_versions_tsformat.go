package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// legacySpaceFormLayout matches the `datetime('now','subsec')` /
// `datetime('now')` default SQLite emits for TEXT columns declared
// with that DEFAULT: a space-separated date/time with an OPTIONAL
// millisecond fraction, no zone marker (SQLite's `now` modifier is
// UTC). The "9" fractional placeholders make the fraction optional on
// parse, matching both `2026-01-01 00:00:05.123` and
// `2026-01-01 00:00:05`. Go returns UTC when a layout carries no zone
// verb, which matches what `datetime('now', ...)` actually produced.
const legacySpaceFormLayout = "2006-01-02 15:04:05.999999999"

// migrationNormalizeMemoryVersionsTsformat (v141) rewrites every
// memory_versions.written_at value into the fixed-width, lex-sortable
// tsformat.Layout (#1073a).
//
// Why this is needed: written_at has been populated by three
// incompatible call sites over the table's history —
//
//  1. internal/memory/versions.go's RecordVersion, which formatted
//     with time.RFC3339Nano. RFC3339Nano TRIMS trailing zero
//     fractional digits and drops the fraction entirely on a whole
//     second, so two rows one second minus a hair apart can render at
//     different string widths (`...05Z` vs `...05.500000000Z`).
//  2. internal/api/agent_persona.go's persona version insert, which
//     omits written_at and falls back to the column's
//     `DEFAULT (datetime('now','subsec'))` — SQLite's space-separated
//     form (`2026-01-01 00:00:05.123`, no 'T', no 'Z').
//  3. Rows already migrated to a prior fixed-width attempt or written
//     by an already-fixed binary — tsformat.Layout itself, which this
//     migration must treat as a no-op.
//
// Because ORDER BY written_at and the keyset-cursor comparison in
// internal/api/memory_versions_list_handler.go both compare
// written_at as TEXT, mixing these three shapes silently corrupts
// sort order: '.' (0x2E) sorts below 'Z' (0x5A) and ' ' (0x20) sorts
// below both, so a row's apparent position depends on which call
// site happened to write it rather than the time it encodes. PR
// #1156's keyset pagination inherited this corruption — a page
// boundary landing on a mixed-format pair can skip or repeat rows.
//
// This migration walks every row once, parses written_at against the
// two legacy shapes (RFC3339Nano covers both the fractional and
// whole-second 'Z' forms; the space-form layout covers the SQLite
// DEFAULT), and rewrites it via tsformat.Format. Rows already in
// tsformat.Layout round-trip to themselves and are skipped (no
// write). A row whose value matches neither shape is left untouched
// and logged at WARN — see the "unparseable" counter below; as of
// authoring, no such rows are known to exist, but the migration must
// not fail the whole upgrade over one corrupt audit-trail row when
// the row itself is otherwise harmless (append-only, never read by
// primary key).
func migrationNormalizeMemoryVersionsTsformat(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, written_at FROM memory_versions`)
	if err != nil {
		return fmt.Errorf("select memory_versions: %w", err)
	}
	type pending struct {
		id  string
		old string
	}
	var toNormalize []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.old); err != nil {
			rows.Close()
			return fmt.Errorf("scan memory_versions row: %w", err)
		}
		toNormalize = append(toNormalize, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate memory_versions: %w", err)
	}
	rows.Close()

	stmt, err := tx.PrepareContext(ctx, `UPDATE memory_versions SET written_at = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare update: %w", err)
	}
	defer stmt.Close()

	var updated, unchanged, unparseable int64
	for _, p := range toNormalize {
		t, ok := parseLegacyMemoryVersionTimestamp(p.old)
		if !ok {
			unparseable++
			logger.Warn("memory_versions written_at: could not parse for tsformat backfill; leaving as-is",
				"id", p.id, "written_at", p.old)
			continue
		}
		normalized := tsformat.Format(t)
		if normalized == p.old {
			unchanged++
			continue
		}
		if _, err := stmt.ExecContext(ctx, normalized, p.id); err != nil {
			return fmt.Errorf("update memory_versions %s: %w", p.id, err)
		}
		updated++
	}

	logger.Info("memory_versions written_at tsformat backfill complete",
		"updated", updated, "already_normalized", unchanged, "unparseable", unparseable)
	return nil
}

// parseLegacyMemoryVersionTimestamp tries every known written_at shape
// in turn and returns the parsed instant. time.RFC3339Nano covers both
// tsformat.Layout itself (fixed 9-digit fraction) and the older
// RFC3339Nano output (variable-width fraction, or none on a whole
// second) since Go's "9"-fraction layout verbs make the fractional
// part optional on parse. legacySpaceFormLayout covers the SQLite
// `datetime('now'[, 'subsec'])` DEFAULT form.
func parseLegacyMemoryVersionTimestamp(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(legacySpaceFormLayout, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}
