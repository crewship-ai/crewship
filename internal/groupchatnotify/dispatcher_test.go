package groupchatnotify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type delivery struct {
	user, event string
	payload     any
}
type fakeHub struct {
	fail  bool
	calls []delivery
}

func (h *fakeHub) BroadcastChannelContext(_ context.Context, prefix, user, event string, payload any) error {
	if h.fail {
		return errors.New("hub stopped")
	}
	if prefix != "user" {
		panic("private event sent outside user channel")
	}
	h.calls = append(h.calls, delivery{user, event, payload})
	return nil
}
func fixture(t *testing.T, kind string) (*sql.DB, *groupchat.Store, groupchat.Conversation) {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','workspace')`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"one", "two", "three"} {
		if _, err := db.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, u, u+"@example.test", u); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,'w',?,'MEMBER')`, "wm-"+u, u); err != nil {
			t.Fatal(err)
		}
	}
	store := groupchat.New(db)
	c, err := store.Create(context.Background(), "w", "one", groupchat.CreateInput{Title: "SECRET PROJECT", Kind: kind, MemberIDs: []string{"two"}})
	if err != nil {
		t.Fatal(err)
	}
	return db, store, c
}
func send(t *testing.T, s *groupchat.Store, c groupchat.Conversation, id string) groupchat.Message {
	t.Helper()
	m, _, err := s.Send(context.Background(), "w", "one", c.ID, groupchat.SendInput{ClientID: id, Content: "secret password document"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestProjectionPrivateAudienceAggregationAndReplay(t *testing.T) {
	db, store, c := fixture(t, "group")
	send(t, store, c, "first")
	hub := &fakeHub{}
	d := New(db, hub, nil)
	if err := d.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE kind='message'`); n != 1 {
		t.Fatalf("inbox=%d", n)
	}
	var target, body, payload, title string
	if err := db.QueryRow(`SELECT target_user_id,body_md,payload_json,title FROM inbox_items`).Scan(&target, &body, &payload, &title); err != nil {
		t.Fatal(err)
	}
	if target != "two" || strings.Contains(body+payload+title, "secret") || strings.Contains(body+payload+title, "SECRET") {
		t.Fatalf("unsafe projection: %s %s %s %s", target, body, payload, title)
	}
	for _, call := range hub.calls {
		if call.user == "three" {
			t.Fatal("private invalidation leaked to nonparticipant")
		}
	}
	if _, err := db.Exec(`UPDATE inbox_items SET state='read'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_conversation_outbox SET delivered_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := d.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE state='unread'`) != 0 {
		t.Fatal("replay resurrected read row")
	}
	send(t, store, c, "second")
	if err := d.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE state='unread' AND json_extract(payload_json,'$.last_sequence')=2`) != 1 {
		t.Fatal("new message did not update one conversation item")
	}
	if count(t, db, `SELECT COUNT(*) FROM inbox_items`) != 1 {
		t.Fatal("one item per message instead of per conversation")
	}
}
func TestReadBeforeDeliveryAndRevokedMemberAreSuppressed(t *testing.T) {
	for _, mode := range []string{"read", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			db, store, c := fixture(t, "group")
			m := send(t, store, c, "first")
			if mode == "read" {
				if err := store.MarkRead(context.Background(), "w", "two", c.ID, m.Sequence); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Exec(`DELETE FROM workspace_members WHERE user_id='two'`); err != nil {
					t.Fatal(err)
				}
			}
			hub := &fakeHub{}
			if err := New(db, hub, nil).Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			if count(t, db, `SELECT COUNT(*) FROM inbox_items`) != 0 {
				t.Fatal("read or revoked recipient was notified")
			}
			if mode == "revoked" {
				for _, call := range hub.calls {
					if call.user == "two" {
						t.Fatal("revoked recipient got invalidation")
					}
				}
			}
		})
	}
}
func TestChannelAudienceIncludesWorkspaceMembers(t *testing.T) {
	db, store, c := fixture(t, "channel")
	send(t, store, c, "first")
	if err := New(db, &fakeHub{}, nil).Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM inbox_items`) != 2 {
		t.Fatal("channel omitted workspace participant")
	}
}
func TestFailedBroadcastRetainsOutboxAndRetryIsIdempotent(t *testing.T) {
	db, store, c := fixture(t, "group")
	send(t, store, c, "first")
	hub := &fakeHub{fail: true}
	d := New(db, hub, nil)
	if err := d.Drain(context.Background()); err == nil {
		t.Fatal("expected stopped hub error")
	}
	if count(t, db, `SELECT COUNT(*) FROM workspace_conversation_outbox WHERE delivered_at IS NULL`) != 1 {
		t.Fatal("event lost on delivery failure")
	}
	hub.fail = false
	if err := d.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM inbox_items`) != 1 {
		t.Fatal("retry duplicated item")
	}
	if count(t, db, `SELECT COUNT(*) FROM workspace_conversation_outbox WHERE delivered_at IS NULL`) != 0 {
		t.Fatal("event not acknowledged")
	}
}
func TestReadAfterDeliveryAndStaleEventDoNotResurrect(t *testing.T) {
	db, store, c := fixture(t, "group")
	send(t, store, c, "first")
	second := send(t, store, c, "second")
	d := New(db, &fakeHub{}, nil)
	if err := d.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRead(context.Background(), "w", "two", c.ID, second.Sequence); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE state='unread'`) != 0 {
		t.Fatal("conversation read did not clear inbox")
	}
	if _, err := db.Exec(`UPDATE workspace_conversation_outbox SET delivered_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := d.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE state='unread'`); n != 0 {
		t.Fatal(fmt.Sprintf("replayed read event resurrected %d rows", n))
	}
}

func TestRunStopsOnCanceledContext(t *testing.T) {
	db, _, _ := fixture(t, "group")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { New(db, &fakeHub{}, nil).Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher failed to stop")
	}
}

func TestMutedConversationPreservesUnreadAndRealtime(t *testing.T) {
	for _, kind := range []string{"group", "channel"} {
		t.Run(kind, func(t *testing.T) {
			db, store, c := fixture(t, kind)
			ctx := context.Background()
			if err := store.SetMuted(ctx, "w", "two", c.ID, true); err != nil {
				t.Fatal(err)
			}
			send(t, store, c, "muted")
			hub := &fakeHub{}
			d := New(db, hub, nil)
			if err := d.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE target_user_id='two'`); n != 0 {
				t.Fatalf("muted inbox=%d", n)
			}
			got, err := store.Get(ctx, "w", "two", c.ID)
			if err != nil || !got.Muted || got.UnreadCount != 1 || got.LastReadSequence != 0 {
				t.Fatalf("muted conversation %+v %v", got, err)
			}
			realtime := false
			for _, call := range hub.calls {
				if call.user == "two" && call.event == "conversation.updated" {
					realtime = true
				}
			}
			if !realtime {
				t.Fatal("muted user missed realtime")
			}
			history, err := store.Messages(ctx, "w", "two", c.ID, 0, 10)
			if err != nil || len(history) != 1 {
				t.Fatalf("history %+v %v", history, err)
			}
			if _, _, err := store.Send(ctx, "w", "two", c.ID, groupchat.SendInput{ClientID: "my-message", Content: "Still muted"}); err != nil {
				t.Fatal(err)
			}
			got, err = store.Get(ctx, "w", "two", c.ID)
			if err != nil || !got.Muted || got.UnreadCount != 1 {
				t.Fatalf("send changed mute %+v %v", got, err)
			}
			if err := d.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.SetMuted(ctx, "w", "two", c.ID, false); err != nil {
				t.Fatal(err)
			}
			send(t, store, c, "unmuted")
			if err := d.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE target_user_id='two' AND state='unread'`); n != 1 {
				t.Fatalf("unmuted inbox=%d", n)
			}
			if err := store.MarkRead(ctx, "w", "two", c.ID, 3); err != nil {
				t.Fatal(err)
			}
			got, err = store.Get(ctx, "w", "two", c.ID)
			if err != nil || got.Muted || got.UnreadCount != 0 {
				t.Fatalf("read %+v %v", got, err)
			}
		})
	}
}

func TestRestoredInboxIdentityClearsReadMarker(t *testing.T) {
	db, store, c := fixture(t, "group")
	ctx := context.Background()
	send(t, store, c, "old")
	d := New(db, nil, nil)
	if err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	// Fork restore remaps the PK while its source still identifies this conversation.
	if _, err := db.Exec(`UPDATE inbox_items SET id='restored-inbox-id',state='read' WHERE target_user_id='two'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads(inbox_item_id,user_id,read_at) VALUES('restored-inbox-id','two','2026-09-06T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	send(t, store, c, "new")
	if err := d.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id='restored-inbox-id'`); n != 0 {
		t.Fatalf("stale restored read markers=%d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE id='restored-inbox-id' AND state='unread'`); n != 1 {
		t.Fatalf("restored inbox not unread: %d", n)
	}
}

func TestHumanInboxCanonicalChatLinkAndLegacyUpgrade(t *testing.T) {
	db, store, c := fixture(t, "group")
	ctx := context.Background()
	send(t, store, c, "first")
	dispatcher := New(db, nil, nil)
	if err := dispatcher.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var href string
	if err := db.QueryRow(`SELECT json_extract(payload_json,'$.chat_url') FROM inbox_items WHERE target_user_id='two'`).Scan(&href); err != nil || href != groupchat.URL(c.ID) {
		t.Fatalf("canonical href=%q err=%v", href, err)
	}
	legacy := "/conversations?conversation=" + c.ID
	if _, err := db.Exec(`UPDATE inbox_items SET payload_json=json_set(payload_json,'$.chat_url',?)`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_conversation_outbox SET delivered_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT json_extract(payload_json,'$.chat_url') FROM inbox_items WHERE target_user_id='two'`).Scan(&href); err != nil || href != legacy {
		t.Fatalf("idempotent historical replay changed old link=%q err=%v", href, err)
	}
	// Existing links remain valid via the UI redirect. A new event replaces the
	// aggregate with the canonical URL without changing its private audience.
	send(t, store, c, "second")
	if err := dispatcher.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT json_extract(payload_json,'$.chat_url') FROM inbox_items WHERE target_user_id='two'`).Scan(&href); err != nil || href != groupchat.URL(c.ID) {
		t.Fatalf("upgrade href=%q err=%v", href, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE target_user_id<>'two' OR title LIKE '%SECRET%' OR body_md LIKE '%secret%'`); n != 0 {
		t.Fatalf("private metadata/audience leaked: %d", n)
	}
}
