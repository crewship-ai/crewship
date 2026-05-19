package episodic

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// indexer.go — Indexer.Start.
//
// The Start loop is the sweeper goroutine the server runs once at
// process start. Sibling tests cover sweepOnce + qualifies + IndexOne;
// Start itself was untested despite carrying two contracts the rest
// of the system relies on:
//
//   1. KICK-OFF: "Kick off once immediately so tests and short-lived
//      processes don't have to wait a full interval" — pin so a
//      refactor that moved the initial sweep into the loop would
//      surface here (tests would have to wait `poll` for the first
//      sweep instead of getting it for free at startup).
//   2. CTX-CANCEL: Start must return promptly on ctx.Done(); a
//      regression that only watched ticker.C would leak the goroutine
//      forever across process shutdown.
//
// The function is BLOCKING (unlike StartTimeoutSweeper which spawns
// its own goroutine), so the test runs it in a goroutine and uses
// Done channels to verify lifecycle without arbitrary sleeps.
// ---------------------------------------------------------------------------

// concurrentCountingEmbedder mirrors stubEmbedder but tracks invocations so
// tests can assert that the sweep actually called Embed (proves the
// indexing path ran end-to-end, not just the SQL select).
type concurrentCountingEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (c *concurrentCountingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return []float32{0.1, 0.2, 0.3, 0.4}, nil
}
func (c *concurrentCountingEmbedder) Dim() int      { return 4 }
func (c *concurrentCountingEmbedder) Model() string { return "test-embedder" }
func (c *concurrentCountingEmbedder) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// embeddingsCountTotal counts ALL journal_embeddings rows. Used by
// tests that need to assert "no work happened" or "exactly N rows
// landed" without filtering by entry_id.
func embeddingsCountTotal(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_embeddings`).Scan(&n); err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	return n
}

func TestIndexerStart_KicksOffInitialSweepImmediately(t *testing.T) {
	// Source comment: "Kick off once immediately so tests and short-
	// lived processes don't have to wait a full interval". Pin by
	// using a poll interval LONGER than the test timeout — if the
	// initial sweep didn't fire, the embedding would never land.
	db := openTestDB(t)
	db.SetMaxOpenConns(1) // :memory: per-connection schema workaround
	defer db.Close()

	// Seed an embeddable entry BEFORE Start.
	insertEntry(t, db, journal.Entry{
		ID:          "j-initial",
		WorkspaceID: "ws_test",
		AgentID:     "a1",
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorAgent,
		Summary:     "needs eyes",
	})

	emb := &concurrentCountingEmbedder{}
	// Poll set to 1 hour — only the initial kick-off can fire within
	// the test budget. If we observe an embedding row, the initial
	// sweep must have run.
	idx := NewIndexer(db, emb, quietLogger(), 1*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		idx.Start(ctx)
		close(done)
	}()

	// Poll for the embedding row — initial sweep should land fast.
	deadline := time.Now().Add(2 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_embeddings WHERE entry_id = 'j-initial'`).Scan(&got); err != nil {
			t.Fatalf("readback: %v", err)
		}
		if got == 1 {
			n = got
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n != 1 {
		t.Errorf("initial-sweep embedding row missing after 2s; Start did not kick off the initial sweep")
	}
	if emb.callCount() < 1 {
		t.Errorf("Embed call count = %d, want ≥ 1 (initial sweep should invoke embedder)", emb.callCount())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of ctx cancel")
	}
}

func TestIndexerStart_ExitsPromptlyOnContextCancel(t *testing.T) {
	// Pin the ctx-cancel branch independently of the kick-off
	// contract. Use a small poll so the loop is actively idling on
	// the select when we cancel.
	db := openTestDB(t)
	db.SetMaxOpenConns(1) // :memory: per-connection schema workaround
	defer db.Close()

	emb := &concurrentCountingEmbedder{}
	idx := NewIndexer(db, emb, quietLogger(), 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		idx.Start(ctx)
		close(done)
	}()

	// Let one or two ticks happen, then cancel.
	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of ctx cancel — ctx.Done branch not honored")
	}
}

func TestIndexerStart_TicksProcessNewlyArrivedEntries(t *testing.T) {
	// Empty DB at Start → initial sweep finds nothing. Insert an
	// entry mid-loop and verify the next tick picks it up. Pin so
	// a regression that ran the initial sweep ONLY (never re-ticked)
	// would surface here.
	db := openTestDB(t)
	// :memory: SQLite is per-connection; with the sweeper goroutine
	// and the main test routine racing for connections, the pool
	// can hand out a fresh blank-schema connection. Pin to one.
	db.SetMaxOpenConns(1)
	defer db.Close()

	emb := &concurrentCountingEmbedder{}
	idx := NewIndexer(db, emb, quietLogger(), 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		idx.Start(ctx)
		close(done)
	}()

	// Let the initial sweep complete on the empty table.
	time.Sleep(50 * time.Millisecond)
	if got := embeddingsCountTotal(t, db); got != 0 {
		t.Fatalf("pre-insert embeddings = %d, want 0", got)
	}

	// Insert an embeddable entry mid-flight.
	insertEntry(t, db, journal.Entry{
		ID:          "j-late",
		WorkspaceID: "ws_test",
		AgentID:     "a1",
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorAgent,
		Summary:     "appeared after Start",
	})

	// Wait for the next tick (≤ ~2 polls) + sweep work.
	deadline := time.Now().Add(2 * time.Second)
	var indexed int
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_embeddings WHERE entry_id = 'j-late'`).Scan(&indexed); err != nil {
			t.Fatalf("readback: %v", err)
		}
		if indexed == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if indexed != 1 {
		t.Errorf("late entry never indexed; ticker branch did not re-sweep after the initial kick-off")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of ctx cancel")
	}
}

func TestIndexerStart_AlreadyCancelledContext_ReturnsQuickly(t *testing.T) {
	// Defensive: ctx already cancelled when Start runs. The initial
	// sweep still fires (it's synchronous, before the select), but
	// the loop should exit on the first select pass when it sees
	// ctx.Done. A regression that called time.NewTicker(x.poll) with
	// poll=0 would also panic — pin that we don't hit that path
	// (ticker created with valid poll, then loop exits).
	db := openTestDB(t)
	db.SetMaxOpenConns(1) // :memory: per-connection schema workaround
	defer db.Close()
	emb := &concurrentCountingEmbedder{}
	idx := NewIndexer(db, emb, quietLogger(), 1*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	done := make(chan struct{})
	go func() {
		idx.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Start with pre-cancelled ctx did not return within 1s")
	}
}
