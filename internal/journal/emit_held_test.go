package journal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Background entries refused by a broken chain tail are held and retried, not
// dropped as poison; other workspaces in the same batch still commit, and the
// chain-break observer hears about the broken workspace.
func TestAuditBrokenTailHoldsBackgroundEntries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	writer := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: 10 * time.Millisecond, HeldRetryInterval: 20 * time.Millisecond})
	defer writer.Close()
	var mu sync.Mutex
	var reported []string
	writer.SetChainBreakObserver(func(ctx context.Context, workspace string, err error) {
		if keys, keyErr := SigningKeysForContext(ctx); keyErr != nil || keys == nil {
			t.Errorf("observer context lacks the writer's keys: %v", keyErr)
		}
		if !errors.Is(err, ErrChainIntegrity) {
			t.Errorf("observer error: %v", err)
		}
		mu.Lock()
		reported = append(reported, workspace)
		mu.Unlock()
	})
	emitAuthentic(t, writer, "ws_test", 3)
	var original string
	if err := db.QueryRow(`SELECT summary FROM journal_entries WHERE seq=3 AND workspace_id='ws_test'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE journal_entries SET summary='changed' WHERE seq=3 AND workspace_id='ws_test'`); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := writer.Emit(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Held while broken"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Emit(t.Context(), Entry{WorkspaceID: "unrelated", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Other chain"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='unrelated'`).Scan(&n)
		return n == 1
	})
	time.Sleep(60 * time.Millisecond) // several retry ticks while still broken
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws_test'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("entries written onto a broken tail: %d %v", n, err)
	}
	mu.Lock()
	if len(reported) != 1 || reported[0] != "ws_test" {
		t.Errorf("chain break reports = %v, want one for ws_test", reported)
	}
	mu.Unlock()
	// Repairing the tail (as a reseal would continue it) releases the held entries.
	if _, err := db.Exec(`UPDATE journal_entries SET summary=? WHERE seq=3 AND workspace_id='ws_test'`, original); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws_test'`).Scan(&n)
		return n == 8
	})
	result, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || !result.OK {
		t.Fatalf("held entries did not extend the chain cleanly: %+v %v", result, err)
	}
}

func TestAuditHeldEntriesAreBounded(t *testing.T) {
	writer := &Writer{logger: quietLogger()}
	held := writer.hold(nil, make([]Entry, maxHeldEntries))
	held = writer.hold(held, []Entry{{ID: "newest"}})
	if len(held) != maxHeldEntries || held[len(held)-1].ID != "newest" {
		t.Fatalf("held=%d last=%q, want cap with newest kept", len(held), held[len(held)-1].ID)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
