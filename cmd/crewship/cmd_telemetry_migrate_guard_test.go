package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// A diagnostic command must not upgrade the schema.
//
// openLocalDB used to call Migrate unconditionally with a discarded logger, so
// `crewship doctor` on a not-yet-upgraded database applied every pending
// migration with no pre-migrate snapshot and no ENCRYPTION_KEY loaded. The
// second half is the dangerous one: v152 backfills the journal hash-chain from
// journal.ChainKeyFromEnv(), which derives from an empty string rather than
// erroring, so the entire historical chain is committed under a null-seed key
// and fails verification forever once the server starts with the real key.
//
// This pins the two halves of the contract: an out-of-date database is refused
// with an actionable message, and a fresh one is still initialized.
func TestOpenLocalDB_RefusesToUpgradeAnExistingSchema(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)

	dataDir, err := database.DefaultDataDir()
	if err != nil {
		t.Fatalf("DefaultDataDir: %v", err)
	}
	db, err := database.Open(dataDir.DatabaseURL())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Stand up a _migrations table claiming an ancient applied version, so the
	// full chain reads as pending against a database that already exists.
	ctx := context.Background()
	if _, err := db.DB.ExecContext(ctx, `CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create _migrations: %v", err)
	}
	if _, err := db.DB.ExecContext(ctx,
		`INSERT INTO _migrations (version, name) VALUES (1, 'init')`); err != nil {
		t.Fatalf("seed _migrations: %v", err)
	}
	from, to, pending, err := database.PendingMigrations(ctx, db.DB)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if from != 1 || pending == 0 || to <= from {
		t.Fatalf("fixture is not an out-of-date schema: from=%d to=%d pending=%d", from, to, pending)
	}
	db.Close()

	_, err = openLocalDB(ctx)
	if err == nil {
		t.Fatal("openLocalDB upgraded an out-of-date schema — a diagnostic command must not migrate")
	}
	// The message has to tell the operator what to do, or the refusal is just
	// a worse failure than the silent upgrade it replaced.
	for _, want := range []string{"pending", "crewship start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}

	// Confirm the refusal did not partially apply anything.
	db2, err := database.Open(dataDir.DatabaseURL())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	after, _, _, err := database.PendingMigrations(ctx, db2.DB)
	if err != nil {
		t.Fatalf("PendingMigrations after: %v", err)
	}
	if after != from {
		t.Errorf("schema moved from v%d to v%d despite the refusal", from, after)
	}
}

// The bootstrap case the original comment was about: an empty database must
// still be initialized, or every sub-command fails with "no such table".
func TestOpenLocalDB_InitializesAFreshDatabase(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	db, err := openLocalDB(context.Background())
	if err != nil {
		t.Fatalf("openLocalDB on a fresh data dir: %v", err)
	}
	defer db.Close()
	from, _, pending, err := database.PendingMigrations(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if from == 0 || pending != 0 {
		t.Errorf("fresh database not fully migrated: from=%d pending=%d", from, pending)
	}
}
