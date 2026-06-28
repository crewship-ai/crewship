package pipeline

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newPendingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE pending_runs (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, pipeline_id TEXT NOT NULL,
    pipeline_slug TEXT NOT NULL, inputs_json TEXT NOT NULL DEFAULT '{}',
    tags_json TEXT NOT NULL DEFAULT '[]', metadata_json TEXT NOT NULL DEFAULT '{}',
    tier_override TEXT, priority INTEGER NOT NULL DEFAULT 0, debounce_key TEXT,
    fire_at TEXT NOT NULL, expires_at TEXT, debounce_max_at TEXT,
    status TEXT NOT NULL DEFAULT 'pending', fired_run_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now','subsec')));
CREATE UNIQUE INDEX idx_pending_runs_debounce ON pending_runs (pipeline_id, debounce_key)
    WHERE status='pending' AND debounce_key IS NOT NULL;`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPendingRuns_DueAndPriorityOrder(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	// Two due rows, different priority; higher must come first.
	if _, _, err := s.Enqueue(ctx, PendingRun{ID: "p_lo", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", Priority: 1, FireAt: past}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Enqueue(ctx, PendingRun{ID: "p_hi", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", Priority: 9, FireAt: past}); err != nil {
		t.Fatal(err)
	}
	// A not-yet-due row must be excluded.
	if _, _, err := s.Enqueue(ctx, PendingRun{ID: "p_future", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("want 2 due rows, got %d", len(due))
	}
	if due[0].ID != "p_hi" {
		t.Fatalf("priority order: want p_hi first, got %s", due[0].ID)
	}
}

func TestPendingRuns_DebounceCoalesces(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()

	id1, coalesced1, err := s.Enqueue(ctx, PendingRun{ID: "p1", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(30 * time.Second)})
	if err != nil || coalesced1 {
		t.Fatalf("first enqueue: id=%s coalesced=%v err=%v", id1, coalesced1, err)
	}
	// Second trigger with same key must coalesce into the same row.
	id2, coalesced2, err := s.Enqueue(ctx, PendingRun{ID: "p2", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !coalesced2 || id2 != id1 {
		t.Fatalf("second enqueue should coalesce into %s, got id=%s coalesced=%v", id1, id2, coalesced2)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_runs WHERE status='pending'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("debounce should keep one pending row, got %d", count)
	}
}

func TestPendingRuns_ExpireAndClaim(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	exp := time.Now().Add(-time.Second)

	if _, _, err := s.Enqueue(ctx, PendingRun{ID: "p_exp", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past, ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	n, err := s.ExpireDue(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	// Expired row must not be due.
	due, _ := s.DueRuns(ctx, time.Now(), 10)
	if len(due) != 0 {
		t.Fatalf("expired row should not be due, got %d", len(due))
	}

	// Claim semantics: MarkFired wins once, second claim loses.
	if _, _, err := s.Enqueue(ctx, PendingRun{ID: "p_claim", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past}); err != nil {
		t.Fatal(err)
	}
	won, err := s.MarkFired(ctx, "p_claim", "")
	if err != nil || !won {
		t.Fatalf("first claim should win: won=%v err=%v", won, err)
	}
	wonAgain, _ := s.MarkFired(ctx, "p_claim", "run_x")
	if wonAgain {
		t.Fatal("second claim must lose (already fired)")
	}
}
