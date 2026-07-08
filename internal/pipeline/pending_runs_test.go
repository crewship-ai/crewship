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
	// A :memory: DB is per-connection; pin the pool to one connection so
	// concurrent dispatch goroutines all see the same table.
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE pending_runs (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, pipeline_id TEXT NOT NULL,
    pipeline_slug TEXT NOT NULL, inputs_json TEXT NOT NULL DEFAULT '{}',
    tags_json TEXT NOT NULL DEFAULT '[]', metadata_json TEXT NOT NULL DEFAULT '{}',
    tier_override TEXT, priority INTEGER NOT NULL DEFAULT 0, debounce_key TEXT,
    fire_at TEXT NOT NULL, expires_at TEXT, debounce_max_at TEXT,
    invoking_user_id TEXT,
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

// TestPendingRuns_InvokingUserRoundTrip pins the deferred-run half of
// `to: trigger` (issue #842 Phase 1): the user who enqueued a delayed /
// debounced run is persisted with the pending row and handed back to the
// dispatcher via DueRuns, so a notify step in that run can target the real
// triggering user instead of falling back to a workspace-wide notice.
func TestPendingRuns_InvokingUserRoundTrip(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_u", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		InvokingUserID: "usr_trigger", FireAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("want 1 due row, got %d", len(due))
	}
	if due[0].InvokingUserID != "usr_trigger" {
		t.Errorf("InvokingUserID = %q, want usr_trigger (deferred trigger threading)", due[0].InvokingUserID)
	}
}

// TestPendingRuns_CoalesceAdoptsLatestInvokingUser pins that a debounce
// coalesce adopts the LATEST trigger's invoking user alongside its inputs —
// otherwise a run firing with user B's inputs would notify user A (the
// original enqueuer) on a `to: trigger` step.
func TestPendingRuns_CoalesceAdoptsLatestInvokingUser(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	soon := time.Now().Add(time.Second)

	// User A enqueues a debounced trigger.
	if _, coalesced, err := s.Enqueue(ctx, PendingRun{
		ID: "p_a", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "k1", InvokingUserID: "user_A", FireAt: soon,
	}); err != nil || coalesced {
		t.Fatalf("first enqueue: coalesced=%v err=%v", coalesced, err)
	}
	// User B re-triggers the same key → coalesces into the existing row.
	id, coalesced, err := s.Enqueue(ctx, PendingRun{
		ID: "p_b", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "k1", InvokingUserID: "user_B", FireAt: soon,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !coalesced || id != "p_a" {
		t.Fatalf("second enqueue should coalesce into p_a, got id=%q coalesced=%v", id, coalesced)
	}
	due, err := s.DueRuns(ctx, time.Now().Add(2*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("want 1 coalesced row, got %d", len(due))
	}
	if due[0].InvokingUserID != "user_B" {
		t.Errorf("coalesced InvokingUserID = %q, want user_B (latest triggerer)", due[0].InvokingUserID)
	}
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
