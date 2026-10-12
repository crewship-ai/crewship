package groupchatnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestProjectionAndAcknowledgementFailuresRemainReplayable(t *testing.T) {
	for _, stage := range []struct{ table, operation string }{{"inbox_items", "INSERT"}, {"inbox_item_reads", "DELETE"}, {"workspace_conversation_outbox", "UPDATE"}} {
		t.Run(stage.table, func(t *testing.T) {
			db, store, conversation := fixture(t, "group")
			db.SetMaxOpenConns(1)
			d := New(db, nil, nil)
			send(t, store, conversation, "first")
			if err := d.Drain(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE inbox_items SET state='read'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO inbox_item_reads(inbox_item_id,user_id,read_at) SELECT id,'two','2026-10-02T00:00:00Z' FROM inbox_items`); err != nil {
				t.Fatal(err)
			}
			send(t, store, conversation, "second")
			trigger := fmt.Sprintf(`CREATE TEMP TRIGGER fail_projection BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`, stage.operation, stage.table)
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if err := d.Drain(t.Context()); err == nil {
				t.Fatal("failed persistence was acknowledged")
			}
			if n := count(t, db, `SELECT COUNT(*) FROM workspace_conversation_outbox WHERE delivered_at IS NULL`); n != 1 {
				t.Fatalf("lost retryable outbox: %d", n)
			}
			if stage.table != "workspace_conversation_outbox" {
				if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE state='read'`); n != 1 {
					t.Fatal("partial projection escaped rollback")
				}
				if n := count(t, db, `SELECT COUNT(*) FROM inbox_item_reads`); n != 1 {
					t.Fatal("read marker escaped rollback")
				}
			}
			if _, err := db.Exec(`DROP TRIGGER fail_projection`); err != nil {
				t.Fatal(err)
			}
			if err := d.Drain(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE state='unread'`); n != 1 {
				t.Fatalf("retry did not converge on one unread item: %d", n)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM inbox_item_reads`); n != 0 {
				t.Fatal("retry left stale read marker")
			}
		})
	}
}

type inboxFailureHub struct{ fakeHub }

func (h *inboxFailureHub) BroadcastChannelContext(ctx context.Context, prefix, user, event string, payload any) error {
	if event == "inbox.updated" {
		return errors.New("inbox delivery unavailable")
	}
	return h.fakeHub.BroadcastChannelContext(ctx, prefix, user, event, payload)
}
func TestSecondBroadcastFailureDoesNotAcknowledgeConversationEvent(t *testing.T) {
	db, store, c := fixture(t, "group")
	send(t, store, c, "first")
	if err := New(db, &inboxFailureHub{}, nil).Drain(t.Context()); err == nil {
		t.Fatal("second broadcast failed silently")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM workspace_conversation_outbox WHERE delivered_at IS NULL`); n != 1 {
		t.Fatal("partially delivered event was acknowledged")
	}
	if err := New(db, &fakeHub{}, nil).Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type dispatchLogChannel chan string

func (c dispatchLogChannel) Write(p []byte) (int, error) {
	select {
	case c <- string(p):
	default:
	}
	return len(p), nil
}
func TestDispatcherReportsStorageOutageAndStops(t *testing.T) {
	db, _, _ := fixture(t, "group")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	logs := make(dispatchLogChannel, 8)
	d := New(db, nil, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("dispatcher failed to stop")
		}
	})
	for _, message := range []string{"channel activity projection delayed", "conversation notifications delayed"} {
		select {
		case line := <-logs:
			if !strings.Contains(line, message) {
				t.Fatalf("missing actionable failure %q: %s", message, line)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("storage outage went unreported")
		}
	}
}
