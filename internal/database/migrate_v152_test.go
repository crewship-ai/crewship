package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// TestMigrationV152_BackfillsValidChain lands the pre-chain schema (≤ v148),
// inserts raw unchained journal rows across two workspaces, applies v152, and
// asserts each workspace verifies as a well-formed hash-chain — i.e. the
// backfill reconstructs a chain that journal.VerifyChain accepts, and a later
// tamper is then detectable.
func TestMigrationV152_BackfillsValidChain(t *testing.T) {
	ctx := context.Background()
	// foreign_keys OFF so we can insert journal rows without materializing
	// every FK parent (workspaces/crews/agents/missions).
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 148, logger); err != nil {
		t.Fatalf("migrate to 148: %v", err)
	}

	// Insert unchained rows: ws_a has 3, ws_b has 2, interleaved insert order
	// to prove the backfill orders by (workspace_id, ts, id), not rowid.
	rows := []struct{ id, ws, ts, summary string }{
		{"j1", "ws_a", "2026-01-01T00:00:01.000Z", "a-one"},
		{"j2", "ws_b", "2026-01-01T00:00:02.000Z", "b-one"},
		{"j3", "ws_a", "2026-01-01T00:00:03.000Z", "a-two"},
		{"j4", "ws_a", "2026-01-01T00:00:04.000Z", "a-three"},
		{"j5", "ws_b", "2026-01-01T00:00:05.000Z", "b-two"},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO journal_entries (id, workspace_id, ts, entry_type, actor_type, summary)
			 VALUES (?, ?, ?, 'run.started', 'agent', ?)`,
			r.id, r.ws, r.ts, r.summary); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	// Apply ONLY v152 against the populated v148 schema.
	m152, err := findMigration(152)
	if err != nil {
		t.Fatalf("find v152: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := m152.fn(ctx, tx, logger); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v152: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v152: %v", err)
	}

	for _, ws := range []string{"ws_a", "ws_b"} {
		res, err := journal.VerifyChain(ctx, db, ws)
		if err != nil {
			t.Fatalf("verify %s: %v", ws, err)
		}
		if !res.OK {
			t.Fatalf("backfilled chain for %s broken at seq=%d: %s", ws, res.BrokenSeq, res.Reason)
		}
	}

	// Tamper a backfilled row → detected.
	if _, err := db.ExecContext(ctx, `UPDATE journal_entries SET summary = 'HACKED' WHERE id = 'j3'`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	res, err := journal.VerifyChain(ctx, db, "ws_a")
	if err != nil {
		t.Fatalf("verify tampered: %v", err)
	}
	if res.OK {
		t.Fatalf("tamper of backfilled row went undetected")
	}
	if res.BrokenID != "j3" {
		t.Fatalf("want break at j3, got %s", res.BrokenID)
	}
}
