package inbox

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestInsertTxSharesCallerCommitAndRollback(t *testing.T) {
	db := newInboxTestDB(t)
	item := Item{WorkspaceID: "ws1", Kind: KindMessage, SourceID: "transactional", Title: "Transactional notice"}
	if err := InsertTx(t.Context(), nil, nil, item); err == nil {
		t.Fatal("nil transaction accepted")
	}
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := InsertTx(t.Context(), tx, nil, Item{}); err == nil {
			tx.Rollback()
			t.Fatal("empty envelope silently accepted")
		}
		if err := InsertTx(t.Context(), tx, nil, item); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='transactional'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if commit {
			want = 1
		}
		if count != want {
			t.Fatalf("commit=%v rows=%d", commit, count)
		}
		if err := InsertTx(t.Context(), tx, nil, item); !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("finished transaction accepted: %v", err)
		}
	}
}

func TestTransactionalNotificationWaitsForExplicitPostCommitFanout(t *testing.T) {
	db := newInboxTestDB(t)
	recorder := &recordingNotifier{}
	restore := SetExternalNotifierForTesting(nil)
	defer restore()
	SetExternalNotifier(recorder)
	item := Item{WorkspaceID: "ws1", Kind: KindMessage, SourceID: "post-commit", Title: "Committed notice"}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := InsertTx(t.Context(), tx, nil, item); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls()) != 0 {
		t.Fatal("notification escaped before commit")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	NotifyExternalChannels(t.Context(), item)
	calls := recorder.calls()
	if len(calls) != 1 || calls[0].SourceID != item.SourceID || calls[0].Title != item.Title {
		t.Fatalf("post-commit notification = %#v", calls)
	}
	SetExternalNotifier(nil)
	NotifyExternalChannels(t.Context(), item)
	if len(recorder.calls()) != 1 {
		t.Fatal("disabled notifier still received item")
	}
}

func TestThreadedWriteFailuresDoNotPublishPartialContent(t *testing.T) {
	for _, operation := range []string{"insert", "merge"} {
		t.Run(operation, func(t *testing.T) {
			db := newInboxTestDB(t)
			item := Item{WorkspaceID: "ws1", Kind: KindMessage, SourceID: "source", ThreadKey: "thread", Title: "Original"}
			if operation == "merge" {
				if err := WriteThreaded(t.Context(), db, nil, item); err != nil {
					t.Fatal(err)
				}
			}
			event := "INSERT"
			if operation == "merge" {
				event = "UPDATE"
			}
			if _, err := db.Exec("CREATE TRIGGER reject_write BEFORE " + event + " ON inbox_items BEGIN SELECT RAISE(ABORT,'storage unavailable'); END"); err != nil {
				t.Fatal(err)
			}
			item.Title = "Replacement"
			if err := WriteThreaded(t.Context(), db, nil, item); err == nil {
				t.Fatal("rejected write reported success")
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE title='Replacement'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial replacement persisted: %d %v", count, err)
			}
		})
	}
	db := newInboxTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	item := Item{WorkspaceID: "ws1", Kind: KindMessage, SourceID: "source", ThreadKey: "thread", Title: "Notice"}
	if err := WriteThreaded(t.Context(), db, nil, item); err == nil {
		t.Fatal("closed database accepted threaded write")
	}
	if err := Upsert(t.Context(), db, nil, item); err == nil {
		t.Fatal("closed database accepted upsert")
	}
}

type digestLogSignal struct {
	slog.Handler
	messages chan string
}

func (h digestLogSignal) Handle(ctx context.Context, record slog.Record) error {
	select {
	case h.messages <- record.Message:
	default:
	}
	return nil
}

func TestDigestSchedulerPublishesAndReportsStorageOutage(t *testing.T) {
	db := newDigestTestDB(t)
	now := time.Now()
	seedRun(t, db, "recent", "SUCCEEDED", now.Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	messages := make(chan string, 16)
	logger := slog.New(digestLogSignal{Handler: quietLogger().Handler(), messages: messages})
	StartDigestScheduler(ctx, db, logger, 5*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='digest:ws1'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler never wrote digest")
		}
		time.Sleep(time.Millisecond)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case message := <-messages:
			if message == "inbox: digest sweep failed" {
				cancel()
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler hid storage outage")
		}
	}
}

func TestDigestDefaultsAndUnavailableStore(t *testing.T) {
	if err := RunDigestSweepOnce(t.Context(), nil, nil, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	db := newDigestTestDB(t)
	if err := RunDigestSweepOnce(t.Context(), db, nil, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	StartDigestScheduler(ctx, db, nil, 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RunDigestSweepOnce(t.Context(), db, nil, time.Now(), 0); err == nil {
		t.Fatal("failed digest scan reported success")
	}
}
