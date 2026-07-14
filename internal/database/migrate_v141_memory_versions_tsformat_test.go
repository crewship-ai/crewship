package database

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// TestMigrateV141_MemoryVersionsTsformatBackfill asserts the backfill
// normalizes every legacy written_at shape into tsformat.Layout
// (#1073a):
//
//   - RFC3339Nano with a fraction ("...05.500000000Z" or a
//     trailing-zero-trimmed variant like "...05.5Z")
//   - RFC3339 whole-second, no fraction at all ("...05Z") — what
//     internal/memory/versions.go's old `time.RFC3339Nano` writer
//     produced whenever the wall clock landed on an exact second
//   - the space-form `datetime('now','subsec')` DEFAULT
//     ("2026-01-01 00:00:05.123", no 'T', no 'Z') that
//     internal/api/agent_persona.go's persona insert relied on
//
// A row already in tsformat.Layout must round-trip unchanged (the
// migration is idempotent — re-running it against an
// already-migrated table, or a mix of old and new rows, is a no-op
// for the rows that need no change).
func TestMigrateV141_MemoryVersionsTsformatBackfill(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v141.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, migLogger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)

	seed := func(id, writtenAt string) {
		mustExec(t, db.DB, `
			INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_at, written_by, payload_ref)
			VALUES (?, 'ws1', 'AGENT.md', 'agent', 'sha-'||?, 10, ?, 'test', '/blobs/'||?)`,
			id, id, writtenAt, id)
	}

	const (
		rfc3339NanoFraction = "2026-03-04T10:20:05.500000000Z"
		rfc3339NanoTrimmed  = "2026-03-04T10:20:06.5Z"
		rfc3339WholeSecond  = "2026-03-04T10:20:07Z"
		spaceFormWithSubsec = "2026-03-04 10:20:08.123"
		spaceFormNoSubsec   = "2026-03-04 10:20:09"
		alreadyTsformat     = "2026-03-04T10:20:10.000000000Z"
		unparseableJunk     = "not-a-timestamp"
	)

	seed("mv-frac", rfc3339NanoFraction)
	seed("mv-trimmed", rfc3339NanoTrimmed)
	seed("mv-whole", rfc3339WholeSecond)
	seed("mv-space-subsec", spaceFormWithSubsec)
	seed("mv-space-nosubsec", spaceFormNoSubsec)
	seed("mv-already", alreadyTsformat)
	seed("mv-junk", unparseableJunk)

	// Re-run the backfill directly (bypassing the _migrations
	// idempotency gate) the same way migrate_v136_head_backfill_test.go
	// exercises migrationHeadVersionBackfill: these rows are meant to
	// simulate data that existed BEFORE the migration ran, so we invoke
	// the fn again against them explicitly.
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := migrationNormalizeMemoryVersionsTsformat(context.Background(), tx, migLogger); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	get := func(id string) string {
		var v string
		if err := db.DB.QueryRow(`SELECT written_at FROM memory_versions WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return v
	}

	mustParsedTsformat := func(label, raw string, wantInstant time.Time) {
		t.Helper()
		want := tsformat.Format(wantInstant)
		if len(raw) != len(want) {
			t.Errorf("%s: written_at = %q (len %d), want fixed-width tsformat (len %d)",
				label, raw, len(raw), len(want))
		}
		got, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t.Fatalf("%s: written_at %q did not parse: %v", label, raw, err)
		}
		if !got.Equal(wantInstant) {
			t.Errorf("%s: parsed instant = %v, want %v", label, got, wantInstant)
		}
		if raw != want {
			t.Errorf("%s: written_at = %q, want %q", label, raw, want)
		}
	}

	mustParsedTsformat("mv-frac", get("mv-frac"), time.Date(2026, 3, 4, 10, 20, 5, 500000000, time.UTC))
	mustParsedTsformat("mv-trimmed", get("mv-trimmed"), time.Date(2026, 3, 4, 10, 20, 6, 500000000, time.UTC))
	mustParsedTsformat("mv-whole", get("mv-whole"), time.Date(2026, 3, 4, 10, 20, 7, 0, time.UTC))
	mustParsedTsformat("mv-space-subsec", get("mv-space-subsec"), time.Date(2026, 3, 4, 10, 20, 8, 123000000, time.UTC))
	mustParsedTsformat("mv-space-nosubsec", get("mv-space-nosubsec"), time.Date(2026, 3, 4, 10, 20, 9, 0, time.UTC))
	mustParsedTsformat("mv-already", get("mv-already"), time.Date(2026, 3, 4, 10, 20, 10, 0, time.UTC))

	// Unparseable junk is left untouched rather than dropped or
	// zeroed — an audit-trail row that can't be confidently
	// reinterpreted should not be silently rewritten to a fabricated
	// time.
	if got := get("mv-junk"); got != unparseableJunk {
		t.Errorf("mv-junk: written_at = %q, want unchanged %q", got, unparseableJunk)
	}

	// All six successfully-normalized rows now sort identically to
	// their real chronological order via plain string ORDER BY —
	// the entire point of the fixed-width format.
	rows, err := db.DB.Query(`
		SELECT id FROM memory_versions
		WHERE id != 'mv-junk'
		ORDER BY written_at ASC`)
	if err != nil {
		t.Fatalf("order query: %v", err)
	}
	defer rows.Close()
	var gotOrder []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotOrder = append(gotOrder, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	wantOrder := []string{
		"mv-frac", "mv-trimmed", "mv-whole",
		"mv-space-subsec", "mv-space-nosubsec", "mv-already",
	}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("order query returned %d rows, want %d: %v", len(gotOrder), len(wantOrder), gotOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Errorf("sorted position %d = %q, want %q (full order: %v)", i, gotOrder[i], wantOrder[i], gotOrder)
		}
	}
}

// TestMigrateV141_MemoryVersionsTsformatBackfill_AppliedDuringUpgrade
// asserts the migration is wired into the registered migrations list
// at v141 and runs automatically on a fresh Migrate() — not just when
// invoked directly, as the primary test above does to simulate
// pre-existing legacy rows.
func TestMigrateV141_MemoryVersionsTsformatBackfill_AppliedDuringUpgrade(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v141-applied.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	silent := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	if err := db.DB.QueryRow(`SELECT name FROM _migrations WHERE version = 141`).Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("migration v141 was not recorded as applied")
		}
		t.Fatalf("read _migrations: %v", err)
	}
	if name != "memory_versions_tsformat_backfill" {
		t.Errorf("v141 name = %q, want memory_versions_tsformat_backfill", name)
	}
}
