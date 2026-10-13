package journal

import (
	"bytes"
	"testing"
	"time"
)

func TestAppendTxConfiguredChainAndRollback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	key := []byte("isolated-configured-journal-test-key")
	w := NewWriter(db, quietLogger(), WriterOptions{ChainKey: key, FlushInterval: time.Hour})
	defer w.Close()
	entry := func() Entry {
		return Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorUser, ActorID: "operator", Summary: "invitation.created"}
	}
	if _, err := w.Emit(t.Context(), entry()); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var observed int
	w.AddCommitObserver(func([]Entry) { observed++ })
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := w.AppendTx(t.Context(), tx, entry())
	if err != nil {
		t.Fatal(err)
	}
	if observed != 0 {
		t.Fatal("premature fanout")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	publish()
	if observed != 1 {
		t.Fatal("missing committed fanout")
	}
	if _, err := w.EmitSync(t.Context(), entry()); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyChainWithKey(t.Context(), db, "ws_test", key)
	if err != nil || !res.OK {
		t.Fatalf("mixed chain %+v err%v", res, err)
	}
	// A failed business transaction discards the chained row and its notification.
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.AppendTx(t.Context(), tx, entry())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws_test'`).Scan(&count)
	if count != 3 {
		t.Fatal("rollback retained entry", count)
	}
	res, err = VerifyChainWithKey(t.Context(), db, "ws_test", key)
	if err != nil || !res.OK {
		t.Fatal(res, err)
	}
	if bytes.Equal(key, ChainKeyFromEnv()) {
		t.Fatal("test must exercise nondefault key")
	}
}

func TestAppendTxConcurrentBatchWriter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	key := []byte("concurrent-configured-journal-test-key")
	w := NewWriter(db, quietLogger(), WriterOptions{ChainKey: key, FlushInterval: time.Hour})
	defer w.Close()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorUser, Summary: "invitation.created"}
	if _, err := w.Emit(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	flushed := make(chan error, 1)
	go func() { flushed <- w.Flush(t.Context()) }()
	publish, err := w.AppendTx(t.Context(), tx, entry)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	publish()
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("batch writer deadlocked with caller transaction")
	}
	// Existing asynchronous Flush acknowledges a drained attempt even when
	// a SQLite write conflict retained the batch. Retry after the caller's
	// transaction has released its lock; AppendTx itself never swallows errors.
	if err := w.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := VerifyChainWithKey(t.Context(), db, "ws_test", key)
	if err != nil || !result.OK {
		t.Fatal(result, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws_test'`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
