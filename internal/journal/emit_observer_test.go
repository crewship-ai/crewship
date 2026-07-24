package journal

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestWriter_CommitObserver_BatchPath verifies the commit observer receives
// every entry that durably commits via the normal batch drain, exactly once.
func TestWriter_CommitObserver_BatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{
		QueueSize:     64,
		FlushSize:     3, // several batches for 7 entries
		FlushInterval: 1 * time.Hour,
	})
	defer w.Close()

	var mu sync.Mutex
	seen := map[string]int{}
	w.SetCommitObserver(func(entries []Entry) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range entries {
			seen[e.ID]++
			if e.WorkspaceID != "ws1" {
				t.Errorf("observed entry with workspace %q, want ws1", e.WorkspaceID)
			}
		}
	})

	ctx := context.Background()
	var want []string
	for i := 0; i < 7; i++ {
		id, err := w.Emit(ctx, Entry{
			WorkspaceID: "ws1",
			Type:        EntryRunStarted,
			ActorType:   ActorAgent,
			Summary:     "obs",
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		want = append(want, id)
	}
	// Flush is a barrier: every entry queued before it has persisted, and the
	// observer runs synchronously inside the drain, so it has fired for all.
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(want) {
		t.Fatalf("observer saw %d distinct entries, want %d", len(seen), len(want))
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Errorf("entry %s observed %d times, want exactly 1", id, seen[id])
		}
	}
}

// TestWriter_CommitObserver_PoisonPathOnlyCommitted verifies that when a
// batch hits per-entry poison isolation, the observer receives ONLY the rows
// that actually committed — never the CHECK-rejected poison row.
func TestWriter_CommitObserver_PoisonPathOnlyCommitted(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// workspace_id CHECK that only 'ws_ok' satisfies — mirrors
	// TestWriter_PersistBatch_RollbackOnPartialFailure.
	if _, err := db.Exec(`
		CREATE TABLE journal_entries (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL CHECK(workspace_id = 'ws_ok'),
			crew_id TEXT, agent_id TEXT, mission_id TEXT,
			ts TEXT NOT NULL,
			entry_type TEXT NOT NULL,
			severity TEXT NOT NULL DEFAULT 'info',
			priority TEXT NOT NULL DEFAULT 'normal',
			actor_type TEXT NOT NULL, actor_id TEXT,
			summary TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',
			refs TEXT NOT NULL DEFAULT '{}',
			trace_id TEXT, span_id TEXT, expires_at TEXT,
			seq INTEGER NOT NULL DEFAULT 0,
			prev_hash TEXT NOT NULL DEFAULT '',
			entry_hash TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	w := NewWriter(db, quietLogger(), WriterOptions{
		QueueSize:     16,
		FlushSize:     3, // one batch holds all three
		FlushInterval: 1 * time.Hour,
	})

	var mu sync.Mutex
	var summaries []string
	w.SetCommitObserver(func(entries []Entry) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range entries {
			summaries = append(summaries, e.Summary)
		}
	})

	ctx := context.Background()
	emit := func(ws, summary string) {
		_, _ = w.Emit(ctx, Entry{WorkspaceID: ws, Type: EntryRunStarted, ActorType: ActorAgent, Summary: summary})
	}
	emit("ws_ok", "valid-1")
	emit("ws_bad", "POISON") // CHECK violation → dropped in isolation
	emit("ws_ok", "valid-2")

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(summaries)
	want := []string{"valid-1", "valid-2"}
	if len(summaries) != len(want) {
		t.Fatalf("observer saw %v, want exactly %v (poison must not be observed)", summaries, want)
	}
	for i := range want {
		if summaries[i] != want[i] {
			t.Fatalf("observer saw %v, want %v", summaries, want)
		}
	}
}

// TestWriter_SetCommitObserver_NilAndClear verifies the observer is optional:
// nil (never set) commits cleanly, and clearing to nil stops delivery.
func TestWriter_SetCommitObserver_NilAndClear(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: 10 * time.Millisecond})
	defer w.Close()

	ctx := context.Background()
	// No observer set — must not panic.
	if _, err := w.Emit(ctx, Entry{WorkspaceID: "ws1", Type: EntryRunStarted, ActorType: ActorAgent, Summary: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	var count int
	var mu sync.Mutex
	w.SetCommitObserver(func(entries []Entry) { mu.Lock(); count += len(entries); mu.Unlock() })
	if _, err := w.Emit(ctx, Entry{WorkspaceID: "ws1", Type: EntryRunStarted, ActorType: ActorAgent, Summary: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	w.SetCommitObserver(nil) // clear
	if _, err := w.Emit(ctx, Entry{WorkspaceID: "ws1", Type: EntryRunStarted, ActorType: ActorAgent, Summary: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("observer delivered %d entries, want 1 (only the middle emit, after set and before clear)", count)
	}
}
