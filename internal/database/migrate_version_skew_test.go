package database

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrate_RefusesNewerSchema pins the version-skew guard: migrations are
// forward-only, so an OLD binary booting over a DB that a NEWER binary
// already migrated (max applied version > the highest version this binary
// knows) must REFUSE to start rather than silently run against a schema it
// doesn't understand. The error must be actionable (name both versions +
// the recovery paths).
func TestMigrate_RefusesNewerSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// Simulate a DB migrated by a newer Crewship: the _migrations table
	// carries a version this binary has never heard of.
	future := maxKnownMigrationVersion() + 5
	if _, err := db.ExecContext(ctx, `CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO _migrations (version, name) VALUES (?, 'from_the_future')`, future); err != nil {
		t.Fatal(err)
	}

	err = Migrate(ctx, db, newTestLogger())
	if err == nil {
		t.Fatal("Migrate must refuse to run against a newer schema, got nil error")
	}
	msg := err.Error()
	for _, want := range []string{"newer", "upgrade"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("error message missing %q hint: %q", want, msg)
		}
	}
}

// TestMigrate_AllowsCurrentAndOlder confirms the guard does NOT fire on a
// DB at or below the binary's known version — a freshly migrated DB
// re-migrates idempotently, and the guard is a no-op on a fresh (empty) DB.
func TestMigrate_AllowsCurrentAndOlder(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// Fresh DB → full migrate must succeed (guard sees nothing applied).
	if err := Migrate(ctx, db, newTestLogger()); err != nil {
		t.Fatalf("fresh Migrate: %v", err)
	}
	// Re-run: DB now at exactly maxKnownMigrationVersion → guard must pass.
	if err := Migrate(ctx, db, newTestLogger()); err != nil {
		t.Fatalf("idempotent re-Migrate at current version: %v", err)
	}
}
