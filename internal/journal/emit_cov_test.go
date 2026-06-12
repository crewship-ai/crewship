package journal

// Coverage tests for emit.go — Flush on a closed writer, persistBatch
// error branches (marshal failures, tx begin failure, insert failure)
// and Emit's validation rejection.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFlush_AfterClose_ReturnsNil(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The writer is stopped; Flush must short-circuit on w.closed and
	// report success (everything queued so far is already durable).
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after Close = %v, want nil", err)
	}
}

func TestFlush_CancelledContextWhileQueueBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With an already-cancelled context Flush may either enqueue the
	// barrier (and then return ctx.Err from the ack wait) or bail on the
	// first select; both are valid, but it must NOT hang and must return
	// either nil (barrier raced through) or context.Canceled.
	err := w.Flush(ctx)
	if err != nil && err != context.Canceled {
		t.Fatalf("Flush(cancelled ctx) = %v, want nil or context.Canceled", err)
	}
}

func TestEmit_ValidationErrorIsSynchronous(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()

	if _, err := w.Emit(context.Background(), Entry{ // no workspace
		Type: EntryPeerConversation, ActorType: ActorAgent, Summary: "x",
	}); err == nil || !strings.Contains(err.Error(), "workspace_id") {
		t.Fatalf("want workspace_id validation error, got %v", err)
	}
}

func TestPersistBatch_PayloadMarshalError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()

	err := w.persistOne(context.Background(), Entry{
		ID: "p1", WorkspaceID: "ws_test", TS: time.Now(),
		Type: EntryPeerConversation, Severity: SeverityInfo,
		ActorType: ActorAgent, Summary: "bad payload",
		Payload: map[string]any{"oops": make(chan int)}, // unmarshalable
	})
	if err == nil || !strings.Contains(err.Error(), "marshal payload") {
		t.Fatalf("want marshal payload error, got %v", err)
	}
	// The failed row must not exist.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id='p1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("failed persist left a row behind")
	}
}

func TestPersistBatch_RefsMarshalError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()

	err := w.persistOne(context.Background(), Entry{
		ID: "p2", WorkspaceID: "ws_test", TS: time.Now(),
		Type: EntryPeerConversation, Severity: SeverityInfo,
		ActorType: ActorAgent, Summary: "bad refs",
		Refs: map[string]any{"oops": func() {}}, // unmarshalable
	})
	if err == nil || !strings.Contains(err.Error(), "marshal refs") {
		t.Fatalf("want marshal refs error, got %v", err)
	}
}

func TestPersistBatch_BeginTxErrorOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	w := NewWriter(db, quietLogger(), WriterOptions{})
	_ = w.Close()
	db.Close()

	err := w.persistOne(context.Background(), Entry{
		ID: "p3", WorkspaceID: "ws_test", TS: time.Now(),
		Type: EntryPeerConversation, Severity: SeverityInfo,
		ActorType: ActorAgent, Summary: "closed db",
	})
	if err == nil || !strings.Contains(err.Error(), "begin tx") {
		t.Fatalf("want begin tx error, got %v", err)
	}
}

func TestPersistBatch_DuplicateIDInsertErrorRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{})
	defer w.Close()

	mk := func(id string) Entry {
		return Entry{
			ID: id, WorkspaceID: "ws_test", TS: time.Now(),
			Type: EntryPeerConversation, Severity: SeverityInfo,
			ActorType: ActorAgent, Summary: "dup " + id,
		}
	}
	// Same primary key twice in one batch → second insert violates the
	// PK constraint, whole batch rolls back.
	err := w.persistBatch(context.Background(), []Entry{mk("dup_1"), mk("dup_1")})
	if err == nil || !strings.Contains(err.Error(), "insert") {
		t.Fatalf("want insert error, got %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id='dup_1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rollback failed: %d rows survived a failed batch", n)
	}
}

func TestNewWriter_NilLoggerDefaults(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, nil, WriterOptions{})
	defer w.Close()
	if w.logger == nil {
		t.Fatal("nil logger should default to slog.Default")
	}
	// The writer must still work end to end.
	if _, err := w.Emit(context.Background(), Entry{
		WorkspaceID: "ws_test", Type: EntryPeerConversation,
		ActorType: ActorAgent, Summary: "works",
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 persisted row, got %d", n)
	}
}
