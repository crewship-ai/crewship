package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
)

// #2294: internal/pipeline/store.go used to write pipelines.{created_at,
// updated_at,last_invoked_at} and pipeline_versions.created_at with
// time.RFC3339Nano, which trims trailing zero fractional digits. Two
// instants in the same wall-clock second can then serialise to
// different-length strings, and List's `ORDER BY COALESCE(last_invoked_at,
// created_at) DESC` — a plain TEXT comparison — sorts them wrong: 'Z'
// (0x5A) sorts after '0' (0x30), so the shorter (more-trimmed) string of
// the EARLIER instant sorts after the longer string of a LATER one.
//
// Migration 20260903190851 pads every existing row to the fixed 9-digit
// width the store now writes going forward (internal/tsformat.Format), so
// an old (trimmed) row and a new (fixed-width) row compare correctly
// against each other.
//
// TestMigrateBackfillsPipelinesTimestampFixedWidth drives the real
// migration against a populated DB, the same way
// TestMigrateBackfillsMissionsOwnerDelegate does: apply every migration
// once, seed rows in the pre-fix (trimmed) shape directly, clear just this
// migration's _migrations marker, and re-run Migrate.
func TestMigrateBackfillsPipelinesTimestampFixedWidth(t *testing.T) {
	db := migrateChainSetup(t)

	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_ts', 'WS', 'ws-ts')`)

	seedPipeline := func(id, createdAt, updatedAt string, lastInvokedAt sql.NullString) {
		execMigrationFixture(t, db, `
			INSERT INTO pipelines (id, workspace_id, slug, name, dsl_version, definition_json, definition_hash,
				last_invoked_at, created_at, updated_at)
			VALUES (?, 'ws_ts', ?, ?, '1.0', '{}', 'hash-'||?, ?, ?, ?)`,
			id, id, id, id, lastInvokedAt, createdAt, updatedAt)
	}

	const (
		// The collision from the issue: 05:38:18.100000000 trims to "…18.1Z";
		// 05:38:18.100010000 (10µs later) trims to "…18.10001Z" — shorter
		// string, but the EARLIER instant.
		trimmedEarlier = "2026-08-31T05:38:18.1Z"
		trimmedLater   = "2026-08-31T05:38:18.10001Z"
		// A whole-second write (RFC3339Nano drops the fraction entirely when
		// it's exactly zero) must gain a full ".000000000" fraction, not just
		// get left alone or truncated.
		wholeSecond = "2026-08-31T05:38:19Z"
		// Already fixed-width — must round-trip unchanged (idempotent; also
		// covers a partial or re-run of this migration never double-padding).
		alreadyFixed = "2026-08-31T05:38:20.500000000Z"
	)

	seedPipeline("pl-earlier", trimmedEarlier, trimmedEarlier, sql.NullString{String: trimmedEarlier, Valid: true})
	seedPipeline("pl-later", trimmedLater, trimmedLater, sql.NullString{String: trimmedLater, Valid: true})
	seedPipeline("pl-whole", wholeSecond, wholeSecond, sql.NullString{})
	seedPipeline("pl-already", alreadyFixed, alreadyFixed, sql.NullString{String: alreadyFixed, Valid: true})

	execMigrationFixture(t, db, `
		INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash,
			author_type, author_id, created_at)
		VALUES ('plnv-earlier', 'pl-earlier', 1, '{}', 'hash-pl-earlier', 'agent', 'a1', ?)`, trimmedEarlier)

	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260903190851`); err != nil {
		t.Fatalf("clear migration marker: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (pipelines timestamp fixed width): %v", err)
	}

	get := func(id string) (createdAt, updatedAt string, lastInvokedAt sql.NullString) {
		t.Helper()
		if err := db.QueryRow(`SELECT created_at, updated_at, last_invoked_at FROM pipelines WHERE id = ?`, id).
			Scan(&createdAt, &updatedAt, &lastInvokedAt); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return
	}

	wantFixed := func(label, got, want string) {
		t.Helper()
		const wantLen = len("2026-08-31T05:38:18.100000000Z")
		if len(got) != wantLen {
			t.Errorf("%s = %q (len %d), want fixed-width %q (len %d)", label, got, len(got), want, wantLen)
			return
		}
		if got != want {
			t.Errorf("%s = %q, want %q (padding must not change the instant)", label, got, want)
		}
	}

	earlierCreated, earlierUpdated, earlierInvoked := get("pl-earlier")
	wantFixed("pl-earlier created_at", earlierCreated, "2026-08-31T05:38:18.100000000Z")
	wantFixed("pl-earlier updated_at", earlierUpdated, "2026-08-31T05:38:18.100000000Z")
	wantFixed("pl-earlier last_invoked_at", earlierInvoked.String, "2026-08-31T05:38:18.100000000Z")

	laterCreated, _, laterInvoked := get("pl-later")
	wantFixed("pl-later created_at", laterCreated, "2026-08-31T05:38:18.100010000Z")
	wantFixed("pl-later last_invoked_at", laterInvoked.String, "2026-08-31T05:38:18.100010000Z")

	wholeCreated, _, wholeInvoked := get("pl-whole")
	wantFixed("pl-whole created_at", wholeCreated, "2026-08-31T05:38:19.000000000Z")
	if wholeInvoked.Valid {
		t.Errorf("pl-whole last_invoked_at = %v, want NULL to stay NULL (never invoked)", wholeInvoked)
	}

	alreadyCreated, _, alreadyInvoked := get("pl-already")
	if alreadyCreated != alreadyFixed {
		t.Errorf("pl-already created_at = %q, want unchanged %q", alreadyCreated, alreadyFixed)
	}
	if alreadyInvoked.String != alreadyFixed {
		t.Errorf("pl-already last_invoked_at = %q, want unchanged %q", alreadyInvoked.String, alreadyFixed)
	}

	var versionCreated string
	if err := db.QueryRow(`SELECT created_at FROM pipeline_versions WHERE id = 'plnv-earlier'`).Scan(&versionCreated); err != nil {
		t.Fatalf("read pipeline_versions: %v", err)
	}
	wantFixed("plnv-earlier created_at", versionCreated, "2026-08-31T05:38:18.100000000Z")

	// The whole point: once padded, a plain string ORDER BY sorts the
	// colliding pair chronologically instead of backwards.
	rows, err := db.Query(`
		SELECT id FROM pipelines WHERE id IN ('pl-earlier', 'pl-later')
		ORDER BY COALESCE(last_invoked_at, created_at) DESC`)
	if err != nil {
		t.Fatalf("order query: %v", err)
	}
	defer rows.Close()
	var order []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(order) != 2 || order[0] != "pl-later" || order[1] != "pl-earlier" {
		t.Errorf("padded order = %v, want [pl-later pl-earlier] (DESC, chronological)", order)
	}
}

// TestMigrate_PipelinesTimestampFixedWidth_AppliedDuringUpgrade asserts the
// migration is wired into the file-based registry and runs automatically on
// a fresh Migrate() — not just when triggered by re-clearing its marker, as
// the primary test above does to simulate pre-existing legacy rows.
func TestMigrate_PipelinesTimestampFixedWidth_AppliedDuringUpgrade(t *testing.T) {
	db := migrateChainSetup(t)

	var name string
	if err := db.QueryRow(`SELECT name FROM _migrations WHERE version = 20260903190851`).Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("migration 20260903190851 was not recorded as applied")
		}
		t.Fatalf("read _migrations: %v", err)
	}
	if name != "pipelines_timestamp_fixed_width" {
		t.Errorf("migration name = %q, want pipelines_timestamp_fixed_width", name)
	}
}
