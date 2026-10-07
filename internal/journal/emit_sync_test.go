package journal

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// An audit used as a precondition must be visible before returning, including
// to both live-feed bridges, and observer failures must not undo the commit.
func TestEmitSyncCommitsBeforeFanout(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	notified := w.Notify()
	var seen []string
	w.AddCommitObserver(nil)
	w.AddCommitObserver(func(entries []Entry) {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id = ?`, entries[0].ID).Scan(&count); err != nil || count != 1 {
			t.Errorf("observer ran before durable commit: count=%d err=%v", count, err)
		}
		seen = append(seen, "websocket")
	})
	w.AddCommitObserver(func([]Entry) { panic("failed bridge") })
	w.AddCommitObserver(func([]Entry) { seen = append(seen, "notifications") })

	id, err := w.EmitSync(t.Context(), Entry{
		WorkspaceID: "ws_test", Type: EntryCredentialRevealed,
		ActorType: ActorUser, ActorID: "operator", Summary: "credential revealed",
	})
	if err != nil || id == "" {
		t.Fatalf("EmitSync: id=%q err=%v", id, err)
	}
	if !reflect.DeepEqual(seen, []string{"websocket", "notifications"}) {
		t.Fatalf("observer order/continuation: %v", seen)
	}
	select {
	case <-notified:
	default:
		t.Fatal("synchronous commit did not wake the live feed")
	}
	var seq int
	var hash string
	if err := db.QueryRow(`SELECT seq, entry_hash FROM journal_entries WHERE id = ?`, id).Scan(&seq, &hash); err != nil {
		t.Fatal(err)
	}
	if seq != 1 || hash == "" {
		t.Fatalf("synchronous audit was not chained: seq=%d hash=%q", seq, hash)
	}
	result, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || !result.OK {
		t.Fatalf("verify synchronous chain: result=%+v err=%v", result, err)
	}
}

func TestEmitSyncFailsWithoutPublishing(t *testing.T) {
	for _, failure := range []string{"invalid entry", "canceled context", "database constraint"} {
		t.Run(failure, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
			defer w.Close()
			notified := w.Notify()
			published := false
			w.AddCommitObserver(func([]Entry) { published = true })
			entry := Entry{ID: "audit", WorkspaceID: "ws_test", Type: EntryCredentialRevealed, ActorType: ActorUser, Summary: "credential revealed"}
			ctx := t.Context()
			switch failure {
			case "invalid entry":
				entry.WorkspaceID = ""
			case "canceled context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "database constraint":
				if _, err := db.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON journal_entries BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			id, err := w.EmitSync(ctx, entry)
			if err == nil || id != "" {
				t.Fatalf("audit failure reported success: id=%q err=%v", id, err)
			}
			if failure == "canceled context" && !errors.Is(err, context.Canceled) {
				t.Errorf("cancellation cause lost: %v", err)
			}
			if published {
				t.Error("uncommitted audit was published")
			}
			select {
			case <-notified:
				t.Error("failed audit woke the live feed")
			default:
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed audit persisted: count=%d err=%v", count, err)
			}
		})
	}
}
