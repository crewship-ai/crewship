package journal

import (
	"database/sql"
	"errors"
	"os"
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
	db := openReplayTestDB(t)
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
	db := openReplayTestDB(t)
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
	db := openReplayTestDB(t)
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

func openReplayTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS workspaces(id TEXT PRIMARY KEY); INSERT OR IGNORE INTO workspaces VALUES('ws_test')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261003120810_run_replay_contexts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestReplayEmitReceiptSurvivesHistoryRemoval(t *testing.T) {
	db := openReplayTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	e := replayEntryFixture()
	if _, _, err := w.EmitReplaySync(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id=?`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := w.EmitReplaySync(t.Context(), e); err != nil || inserted {
		t.Fatalf("retained receipt ignored: %v", err)
	}
	e.Summary = "changed after removal"
	if _, _, err := w.EmitReplaySync(t.Context(), e); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("compaction allowed identity rewrite")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil || n != 0 {
		t.Fatal("history recreated from replay")
	}
}

func TestReplayEmitReceiptFailureRollsBackJournal(t *testing.T) {
	db := openReplayTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON journal_replay_receipts BEGIN SELECT RAISE(ABORT,'test receipt failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.EmitReplaySync(t.Context(), replayEntryFixture()); err == nil {
		t.Fatal("receipt failure accepted")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil || n != 0 {
		t.Fatal("journal committed without receipt")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_receipt`); err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := w.EmitReplaySync(t.Context(), replayEntryFixture()); err != nil || !inserted {
		t.Fatalf("retry failed: %v", err)
	}
}
