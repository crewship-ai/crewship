package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// The oldest upgrade anyone can actually perform.
//
// TestUpgradePath_V139WithDataMigratesToHead covers a recent-ish start, which
// is the common case. It is not the worst case: the install that hurts is the
// one that has been running since v1 and skipped every release in between,
// because it exercises the longest chain of migrations interacting with data
// none of them were written against.
//
// This walks from the very first migration with rows present from the start,
// so every later migration sees data that predates it. That is the ordering
// bug this catches and the v139 test cannot: a migration that assumes a column
// introduced after the rows it is rewriting.
//
// It is slower than the rest of the package by design. -short skips it.
func TestUpgradePath_FromV1WithDataMigratesToHead(t *testing.T) {
	if testing.Short() {
		t.Skip("full v1 → HEAD upgrade is slow by design")
	}
	// v152 backfills the journal hash chain from this. Without it the chain is
	// seeded from "" and every pre-migration entry fails verification forever,
	// so running this test keyless would bless a broken upgrade.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))

	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/oldest.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// v1 alone, then data, then everything else.
	if err := applyMigrationsUpTo(ctx, db, 1, quiet); err != nil {
		t.Fatalf("land schema at v1: %v", err)
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", strings.SplitN(q, "(", 2)[0], err)
		}
	}
	// Only tables that exist at v1 — anything else would be testing a
	// different starting point than the one claimed.
	const wsID, userID = "ws-oldest", "user-oldest"
	exec(`INSERT INTO users (id, email) VALUES (?,?)`, userID, "oldest@example.test")
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "Oldest", "oldest")

	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("v1 → HEAD with data: %v", err)
	}

	// The rows survived the whole chain. A migration that rebuilt a table and
	// lost its contents is the failure this asserts against — a schema-only
	// check would pass while the data was gone.
	var email string
	if err := db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&email); err != nil {
		t.Fatalf("user row did not survive the upgrade: %v", err)
	}
	if email != "oldest@example.test" {
		t.Errorf("email = %q after upgrade", email)
	}
	var slug string
	if err := db.QueryRowContext(ctx, `SELECT slug FROM workspaces WHERE id = ?`, wsID).Scan(&slug); err != nil {
		t.Fatalf("workspace row did not survive the upgrade: %v", err)
	}
	if slug != "oldest" {
		t.Errorf("slug = %q after upgrade", slug)
	}

	// And the ledger is complete: every migration this binary declares, except
	// the deferred ones, is recorded.
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM _migrations`).Scan(&applied); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	want := len(migrations) - len(pendingPostDeployDeclared())
	if applied != want {
		t.Errorf("ledger has %d rows, expected %d (declared %d, deferred %d)",
			applied, want, len(migrations), len(pendingPostDeployDeclared()))
	}

	// Re-running must be a no-op. An upgrade that is not idempotent breaks
	// every restart, not just the first.
	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("second Migrate over the upgraded schema failed: %v", err)
	}
}
