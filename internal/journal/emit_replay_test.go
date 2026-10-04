package journal

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func replayEntryFixture() Entry {
	return Entry{ID: "replayed-output-1", WorkspaceID: "ws_test", TS: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC),
		Type: EntryExecOutputChunk, ActorType: ActorAgent, ActorID: "agent", Summary: "Recovered output",
		TraceID: "run-1", SpanID: "record-1", Payload: map[string]any{"output": "already scrubbed", "counter": int64(9007199254740993)}}
}

func TestReplayEmitSurvivesWriterRestartWithoutDuplicate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	db.SetMaxOpenConns(1)
	first := NewWriter(db, quietLogger(), WriterOptions{})
	entry := replayEntryFixture()
	id, inserted, err := first.EmitReplaySync(t.Context(), entry)
	if err != nil || !inserted || id != entry.ID {
		t.Fatalf("first=%s inserted=%v err=%v", id, inserted, err)
	}
	first.Close()
	second := NewWriter(db, quietLogger(), WriterOptions{})
	defer second.Close()
	notify := second.Notify()
	id, inserted, err = second.EmitReplaySync(t.Context(), entry)
	if err != nil || inserted || id != entry.ID {
		t.Fatalf("replay=%s inserted=%v err=%v", id, inserted, err)
	}
	select {
	case <-notify:
		t.Fatal("replay broadcast as a new commit")
	default:
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
	chain, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || !chain.OK {
		t.Fatalf("chain=%+v err=%v", chain, err)
	}
}

func TestReplayEmitRejectsIdentityReuseWithChangedContent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	entry := replayEntryFixture()
	if _, _, err := w.EmitReplaySync(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Entry){
		func(e *Entry) { e.WorkspaceID = "other-workspace" },
		func(e *Entry) { e.TraceID = "other-run" },
		func(e *Entry) { e.Payload = map[string]any{"output": "changed"} },
		func(e *Entry) { e.TS = e.TS.Add(time.Second) },
	} {
		changed := entry
		mutate(&changed)
		if _, _, err := w.EmitReplaySync(t.Context(), changed); !errors.Is(err, ErrReplayConflict) {
			t.Fatalf("conflicting replay accepted: %v", err)
		}
	}
}

func TestReplayEmitConcurrentDeliveryCreatesOneChainEntry(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	db.SetMaxOpenConns(1)
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	var wg sync.WaitGroup
	var inserted atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fresh, err := w.EmitReplaySync(t.Context(), replayEntryFixture())
			if err != nil {
				t.Errorf("replay: %v", err)
			}
			if fresh {
				inserted.Add(1)
			}
		}()
	}
	wg.Wait()
	if inserted.Load() != 1 {
		t.Fatalf("insertions=%d", inserted.Load())
	}
	chain, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || !chain.OK {
		t.Fatalf("chain=%+v err=%v", chain, err)
	}
}
