package testutil

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// dbfixture.go provides shared in-memory SQLite helpers so test
// files can stop reimplementing the same five-line setup. About 25
// files across internal/ today open `:memory:` themselves; future
// tests can pull in this helper instead and stay consistent with
// the foreign-keys-on + auto-cleanup contract.
//
// This file does NOT migrate the existing call sites — that would
// touch too many files in one commit. The helper is additive; old
// tests keep working unchanged.
//
// When to reach for these helpers (versus rolling your own):
//
//   - You need an isolated SQLite instance for the test. Always.
//   - You want foreign keys ON by default (the production server runs
//     with foreign_keys=1; tests that opt out implicitly hide FK bugs).
//   - You don't want to remember to call db.Close() in a defer or
//     t.Cleanup; the helper handles that.
//   - You want to seed a few rows in one call without writing
//     boilerplate `if _, err := db.Exec(...); err != nil { t.Fatal }`.
//
// When NOT to reach for these helpers:
//
//   - You need a FILE-backed SQLite (e.g. testing the WAL recovery
//     path or the data-dir provisioning). Use os.CreateTemp and
//     sql.Open directly; the file path matters and the auto-cleanup
//     semantics are different.
//   - You need to test the migration runner itself. The runner opens
//     its own *sql.DB and you don't want to seed schema underneath it.

// NewMemDB returns an in-memory SQLite database with foreign keys
// enabled. The connection is closed automatically when the test
// (and its subtests) finish via t.Cleanup — callers don't need a
// defer block.
//
// Foreign keys are ON because the production Open() in
// internal/database sets the same pragma; a test that opts out
// hides genuine FK bugs (e.g. a delete that should cascade but
// silently doesn't). Override by calling
// `db.Exec("PRAGMA foreign_keys = OFF")` after construction if a
// specific test needs the legacy behaviour.
func NewMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("testutil.NewMemDB: open: %v", err)
	}
	// SQLite's in-memory database is per-connection. The default
	// connection pool size (0 = unlimited) can spawn new sessions
	// that see an empty schema, which is the canonical "tests pass
	// then fail when run with -parallel" footgun. Pinning to 1
	// connection makes the in-memory DB behave like a per-test
	// singleton — slower under contention, correct.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// NewMemDBWithSchema is NewMemDB plus an ExecContext-free schema
// loader. Each variadic argument is one CREATE TABLE / CREATE INDEX
// / etc. statement; they run in order. Use to spin up a minimal
// schema for a focused test without dragging in the full migration
// chain (which is slow and adds coverage noise).
//
// Statements are run individually so an error message names the
// failing statement instead of the whole batch.
func NewMemDBWithSchema(t *testing.T, statements ...string) *sql.DB {
	t.Helper()
	db := NewMemDB(t)
	for i, stmt := range statements {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("testutil.NewMemDBWithSchema: statement %d failed: %v\nSQL: %s", i, err, stmt)
		}
	}
	return db
}

// SeedRow runs an INSERT (or any single-statement mutation) and
// t.Fatals on error. Saves the four-line check at every test site
// that wants to set up fixture rows without caring about the
// per-row Result.
//
// For multi-row seeding, call repeatedly — the per-call Fatal makes
// it obvious which row failed if the schema drifts.
func SeedRow(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("testutil.SeedRow: %v\nSQL: %s\nargs: %v", err, query, args)
	}
}

// MustExec is SeedRow's INSERT-agnostic alias. Use when the call
// site reads better as "execute this DDL" or "run this update"
// rather than "seed a row" — the underlying behaviour is identical.
func MustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	SeedRow(t, db, query, args...)
}
