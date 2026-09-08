package groupchat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

func fixture(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	handle, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	db := handle.DB
	for _, query := range []string{
		`CREATE TABLE users(id TEXT PRIMARY KEY,full_name TEXT,avatar_url TEXT)`,
		`CREATE TABLE workspaces(id TEXT PRIMARY KEY,deleted_at TEXT)`,
		`CREATE TABLE workspace_members(workspace_id TEXT,user_id TEXT,created_at TEXT DEFAULT '',PRIMARY KEY(workspace_id,user_id))`,
		`CREATE TABLE crews(id TEXT PRIMARY KEY,workspace_id TEXT,deleted_at TEXT,avatar_style TEXT)`,
		`INSERT INTO crews(id,workspace_id) VALUES('crew','w')`,
		`CREATE TABLE agents(id TEXT PRIMARY KEY,workspace_id TEXT,crew_id TEXT DEFAULT 'crew',name TEXT,slug TEXT DEFAULT 'agent',deleted_at TEXT,avatar_seed TEXT,avatar_style TEXT,avatar_svg_hash TEXT)`,
		`CREATE TABLE assignments(id TEXT PRIMARY KEY,status TEXT,cancel_requested_at TEXT)`,
		`CREATE TABLE inbox_items(id TEXT PRIMARY KEY,workspace_id TEXT,kind TEXT,source_id TEXT,state TEXT,payload_json TEXT,updated_at TEXT)`,
		`CREATE TABLE inbox_item_reads(inbox_item_id TEXT,user_id TEXT,read_at TEXT,PRIMARY KEY(inbox_item_id,user_id))`,
		`INSERT INTO workspaces(id) VALUES('w'),('other')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("../database/migrations/20260906211909_workspace_conversations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	agentMigration, err := os.ReadFile("../database/migrations/20260906212441_workspace_conversation_agents.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(agentMigration)); err != nil {
		t.Fatal(err)
	}
	muteMigration, err := os.ReadFile("../database/migrations/20260906213326_workspace_conversation_mute.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(muteMigration)); err != nil {
		t.Fatal(err)
	}
	directMigration, err := os.ReadFile("../database/migrations/20260907085939_workspace_direct_conversations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(directMigration)); err != nil {
		t.Fatal(err)
	}
	activityMigration, err := os.ReadFile("../database/migrations/20260907111020_workspace_conversation_activity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(activityMigration)); err != nil {
		t.Fatal(err)
	}
	continuationMigration, err := os.ReadFile("../database/migrations/20260907131137_workspace_conversation_continuations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(continuationMigration)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		u := fmt.Sprintf("u%d", i)
		if _, err = db.Exec(`INSERT INTO users(id,full_name) VALUES(?,?)`, u, "User "+u); err != nil {
			t.Fatal(err)
		}
		w := "w"
		if i == 104 {
			w = "other"
		}
		if _, err = db.Exec(`INSERT INTO workspace_members(workspace_id,user_id) VALUES(?,?)`, w, u); err != nil {
			t.Fatal(err)
		}
	}
	return New(db), db, path
}
func create(t *testing.T, s *Store, kind string, members ...string) Conversation {
	t.Helper()
	c, err := s.Create(context.Background(), "w", "u0", CreateInput{Title: "Room", Kind: kind, MemberIDs: members})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestConcurrentTenSendersWALAndReopen(t *testing.T) {
	s, db, path := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL %q %v", mode, err)
	}
	if db.Stats().MaxOpenConnections != 5 {
		t.Fatalf("pool=%d", db.Stats().MaxOpenConnections)
	}
	members, err := s.Members(ctx, "w", "u0", c.ID)
	if err != nil || len(members) != 104 {
		t.Fatalf("members=%d err=%v", len(members), err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	durations := []time.Duration{}
	start := make(chan struct{})
	failures := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < 10; j++ {
				before := time.Now()
				input := SendInput{ClientID: fmt.Sprintf("send-%d", j), Content: fmt.Sprintf("human %d message %d", i, j)}
				m, duplicate, err := s.Send(ctx, "w", fmt.Sprintf("u%d", i), c.ID, input)
				if err != nil || duplicate {
					failures <- fmt.Errorf("send %d/%d duplicate=%v err=%v", i, j, duplicate, err)
					return
				}
				again, duplicate, err := s.Send(ctx, "w", fmt.Sprintf("u%d", i), c.ID, input)
				if err != nil || !duplicate || again.ID != m.ID {
					failures <- fmt.Errorf("retry %d/%d duplicate=%v err=%v", i, j, duplicate, err)
					return
				}
				mu.Lock()
				durations = append(durations, time.Since(before))
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	messages, err := s.Messages(ctx, "w", "u99", c.ID, 0, 200)
	if err != nil || len(messages) != 100 {
		t.Fatalf("messages %d err %v", len(messages), err)
	}
	for i, m := range messages {
		if m.Sequence != int64(i+1) {
			t.Fatalf("sequence %d at %d", m.Sequence, i)
		}
	}
	events, err := s.PendingEvents(ctx, 200)
	if err != nil || len(events) != 100 {
		t.Fatalf("events %d err %v", len(events), err)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("10 concurrent senders,104 roster,100 committed+100 duplicate sends; send+retry p50=%s p95=%s max=%s", durations[50], durations[95], durations[99])
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	handle, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	reopened := New(handle.DB)
	page, err := reopened.Messages(ctx, "w", "u99", c.ID, 90, 20)
	if err != nil || len(page) != 10 || page[0].Sequence != 91 {
		t.Fatalf("replay %#v err=%v", page, err)
	}
}
func TestAccessRevocationAndMembership(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "group", "u1")
	for _, user := range []string{"u2", "u104"} {
		t.Run(user, func(t *testing.T) {
			if _, err := s.Get(ctx, "w", user, c.ID); !errors.Is(err, ErrForbidden) {
				t.Fatal(err)
			}
			if _, err := s.Messages(ctx, "w", user, c.ID, 0, 10); !errors.Is(err, ErrForbidden) {
				t.Fatal(err)
			}
			if _, _, err := s.Send(ctx, "w", user, c.ID, SendInput{ClientID: "x", Content: "x"}); !errors.Is(err, ErrForbidden) {
				t.Fatal(err)
			}
			if _, err := s.Members(ctx, "w", user, c.ID); !errors.Is(err, ErrForbidden) {
				t.Fatal(err)
			}
			if err := s.MarkRead(ctx, "w", user, c.ID, 0); !errors.Is(err, ErrForbidden) {
				t.Fatal(err)
			}
		})
	}
	if err := s.AddMember(ctx, "w", "u1", c.ID, "u2"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, "w", "u0", c.ID, "u104"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, "w", "u0", c.ID, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "w", "u2", c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, "w", "u0", c.ID, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "w", "u2", c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id='w' AND user_id='u1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "w", "u1", c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, "w", "u0", c.ID, "u0"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestIdempotencyRollbackAndReadCursor(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "group", "u1")
	input := SendInput{ClientID: "a", Content: "hello"}
	m, _, err := s.Send(ctx, "w", "u0", c.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Content = "changed"
	if _, _, err = s.Send(ctx, "w", "u0", c.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER fail_outbox BEFORE INSERT ON workspace_conversation_outbox BEGIN SELECT RAISE(ABORT,'outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, "w", "u0", c.ID, SendInput{ClientID: "b", Content: "rollback"}); err == nil {
		t.Fatal("expected failure")
	}
	got, err := s.Get(ctx, "w", "u1", c.ID)
	if err != nil || got.LastSequence != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = db.Exec(`INSERT INTO inbox_items(id,workspace_id,kind,source_id,state,payload_json) VALUES('i','w','message',?,'unread','{"last_sequence":1}')`, "conversation_"+c.ID+"_u1"); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkRead(ctx, "w", "u1", c.ID, m.Sequence); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkRead(ctx, "w", "u1", c.ID, 0); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, "w", "u1", c.ID)
	if err != nil || got.LastReadSequence != 1 || got.UnreadCount != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM inbox_items WHERE id='i'`).Scan(&state); err != nil || state != "read" {
		t.Fatalf("state %s %v", state, err)
	}
	if err = s.MarkRead(ctx, "w", "u1", c.ID, 2); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSendingDoesNotMarkUnseenHistoryRead(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	if _, _, err := s.Send(ctx, "w", "u0", c.ID, SendInput{ClientID: "other", Content: "Unread earlier message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items(id,workspace_id,kind,source_id,state,payload_json) VALUES('i','w','message',?,'unread','{"last_sequence":1}')`, "conversation_"+c.ID+"_u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, "w", "u1", c.ID, SendInput{ClientID: "mine", Content: "Sent in background"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "w", "u1", c.ID)
	if err != nil || got.LastReadSequence != 0 || got.UnreadCount != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM inbox_items WHERE id='i'`).Scan(&state); err != nil || state != "unread" {
		t.Fatalf("state %s %v", state, err)
	}
	list, err := s.ListPage(ctx, "w", "u1", 1, 0)
	if err != nil || len(list) != 1 || list[0].UnreadCount != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	next, err := s.ListPage(ctx, "w", "u1", 1, 1)
	if err != nil || len(next) != 0 {
		t.Fatalf("next %+v %v", next, err)
	}
}
