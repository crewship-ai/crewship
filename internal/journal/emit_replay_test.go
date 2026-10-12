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

func TestReplayReceiptSurvivesPruningAndLostSourceACK(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	db.SetMaxOpenConns(1)
	first := NewWriter(db, quietLogger(), WriterOptions{})
	entry := replayEntryFixture()
	if _, fresh, err := first.EmitReplaySync(t.Context(), entry); err != nil || !fresh {
		t.Fatalf("first fresh=%v err=%v", fresh, err)
	}
	first.Close()
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id=?`, entry.ID); err != nil {
		t.Fatal(err)
	}
	second := NewWriter(db, quietLogger(), WriterOptions{})
	defer second.Close()
	var observed atomic.Int32
	second.SetCommitObserver(func(entries []Entry) { observed.Add(int32(len(entries))) })
	id, fresh, err := second.EmitReplaySync(t.Context(), entry)
	if err != nil || fresh || id != entry.ID {
		t.Fatalf("lost-ACK retry id=%q fresh=%v err=%v", id, fresh, err)
	}
	changed := entry
	changed.Payload = map[string]any{"output": "conflicting later projection"}
	if _, _, err := second.EmitReplaySync(t.Context(), changed); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("pruned identity mismatch=%v", err)
	}
	var rows, receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_replay_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || receipts != 1 || observed.Load() != 0 {
		t.Fatalf("rows=%d receipts=%d observers=%d", rows, receipts, observed.Load())
	}
}

func TestReplayReceiptRollsBackWhenJournalAppendFails(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	entry := replayEntryFixture()
	if _, err := w.EmitSync(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	// A conflicting ordinary row makes the journal INSERT fail after the receipt
	// INSERT. The transaction must not leave a receipt for an uncommitted replay.
	if _, _, err := w.EmitReplaySync(t.Context(), entry); err == nil {
		t.Fatal("duplicate ordinary journal ID admitted as a new replay")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_replay_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan receipts=%d err=%v", count, err)
	}
}

func TestReplayTerminalReceiptAuthenticatesScopeAfterPruning(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()
	e := Entry{ID: "run-terminal:receipt-scope", WorkspaceID: "ws_test", AgentID: "agent", TraceID: "receipt-scope", SpanID: "run-terminal:receipt-scope", TS: time.Now().UTC(), Type: EntryRunFailed, Severity: SeverityError, ActorType: ActorSidecar, Summary: "retained terminal"}
	e.Payload = map[string]any{"exit_code": 7}
	if _, _, err := w.EmitReplaySync(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id=?`, e.ID); err != nil {
		t.Fatal(err)
	}
	if kind, exists, err := w.ReplayTerminal(t.Context(), e.ID, "ws_test", "agent", "receipt-scope"); err != nil || !exists || kind != EntryRunFailed {
		t.Fatalf("receipt=%s,%v,%v", kind, exists, err)
	}
	correct, wrong := 7, 8
	if _, exists, err := w.ReplayTerminal(t.Context(), e.ID, "ws_test", "agent", "receipt-scope", &correct); err != nil || !exists {
		t.Fatalf("correct exit rejected: %v %v", exists, err)
	}
	for _, code := range []*int{nil, &wrong} {
		if _, _, err := w.ReplayTerminal(t.Context(), e.ID, "ws_test", "agent", "receipt-scope", code); !errors.Is(err, ErrReplayConflict) {
			t.Fatalf("contradictory exit accepted: %v", err)
		}
	}
	for _, scope := range [][3]string{{"foreign", "agent", "receipt-scope"}, {"ws_test", "foreign", "receipt-scope"}, {"ws_test", "agent", "foreign"}} {
		if _, _, err := w.ReplayTerminal(t.Context(), e.ID, scope[0], scope[1], scope[2]); !errors.Is(err, ErrReplayConflict) {
			t.Fatalf("foreign scope accepted: %v", err)
		}
	}
	e.ID = "retained:receipt-scope:1:0"
	e.SpanID = e.ID
	e.Type = EntryExecOutputChunk
	if _, _, err := w.EmitReplaySync(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.ReplayTerminal(t.Context(), e.ID, "ws_test", "agent", "receipt-scope"); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("chunk acknowledged terminal: %v", err)
	}
}
