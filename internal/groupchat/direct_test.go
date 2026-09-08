package groupchat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestOpenDirectConcurrentCanonicalPairAndWorkspace(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	start := make(chan struct{})
	type result struct {
		c       Conversation
		created bool
		err     error
	}
	results := make(chan result, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			caller, target := "u0", "u1"
			if i%2 == 1 {
				caller, target = target, caller
			}
			c, new, err := s.OpenDirect(ctx, "w", caller, target)
			results <- result{c, new, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	id := ""
	created := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.created {
			created++
		}
		if id == "" {
			id = r.c.ID
		}
		if r.c.ID != id || !r.c.IsDirect || r.c.Kind != "group" || r.c.AccessScope != "participants" {
			t.Fatalf("wrong direct %+v", r.c)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_conversations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conversations=%d %v", count, err)
	}
	members, err := s.Members(ctx, "w", "u0", id)
	if err != nil || len(members) != 2 {
		t.Fatalf("members=%+v %v", members, err)
	}
	for _, u := range []string{"u0", "u1"} {
		if _, err := db.Exec(`INSERT INTO workspace_members(workspace_id,user_id) VALUES('other',?)`, u); err != nil {
			t.Fatal(err)
		}
	}
	other, isNew, err := s.OpenDirect(ctx, "other", "u1", "u0")
	if err != nil || !isNew || other.ID == id {
		t.Fatalf("workspace isolation %+v %v %v", other, isNew, err)
	}
}
func TestOpenDirectReusesHistoryAndCallerState(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := context.Background()
	ordinary := create(t, s, "group", "u1")
	if ordinary.IsDirect {
		t.Fatal("ordinary group flagged direct")
	}
	c, created, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil || !created || c.ID == ordinary.ID {
		t.Fatalf("first %+v %v %v", c, created, err)
	}
	message, _, err := s.Send(ctx, "w", "u0", c.ID, SendInput{ClientID: "hello", Content: "Keep this history"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetMuted(ctx, "w", "u1", c.ID, true); err != nil {
		t.Fatal(err)
	}
	reopened, created, err := s.OpenDirect(ctx, "w", "u1", "u0")
	if err != nil || created || reopened.ID != c.ID || !reopened.Muted || reopened.UnreadCount != 1 || reopened.LastReadSequence != 0 {
		t.Fatalf("reopened %+v %v %v", reopened, created, err)
	}
	if err = s.MarkRead(ctx, "w", "u1", c.ID, 1); err != nil {
		t.Fatal(err)
	}
	reopened, _, err = s.OpenDirect(ctx, "w", "u1", "u0")
	if err != nil || reopened.UnreadCount != 0 || reopened.LastReadSequence != 1 {
		t.Fatalf("read state %+v %v", reopened, err)
	}
	history, err := s.Messages(ctx, "w", "u1", c.ID, 0, 10)
	if err != nil || len(history) != 1 || history[0].ID != message.ID {
		t.Fatalf("history %+v %v", history, err)
	}
	creatorView, _, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil || creatorView.Muted {
		t.Fatalf("peer prefs leaked %+v %v", creatorView, err)
	}
}
func TestDirectACLAndFixedMembershipAfterDeletion(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"u0", "u0"}, {"u0", ""}} {
		if _, _, err := s.OpenDirect(ctx, "w", pair[0], pair[1]); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{"u0", "u104"}, {"u104", "u0"}, {"u0", "missing"}} {
		if _, _, err := s.OpenDirect(ctx, "w", pair[0], pair[1]); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
	}
	c, _, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "w", "u2", c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, "w", "u0", c.ID, "u2"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, "w", "u0", c.ID, "u1"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", c.ID, "missing"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, _, err := s.Send(ctx, "w", "u0", c.ID, SendInput{ClientID: "message", Content: "survives deletion"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_direct_pairs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pair=%d %v", count, err)
	}
	remaining, err := s.Get(ctx, "w", "u0", c.ID)
	if err != nil || !remaining.IsDirect {
		t.Fatalf("deletion changed type %+v %v", remaining, err)
	}
	if err := s.AddMember(ctx, "w", "u0", c.ID, "u2"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	history, err := s.Messages(ctx, "w", "u0", c.ID, 0, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("deletion lost history %+v %v", history, err)
	}
	if _, _, err := s.OpenDirect(ctx, "w", "u0", "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
func TestDirectMembershipRevokedAndBoundedTitle(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE users SET full_name=? WHERE id IN ('u0','u1')`, strings.Repeat("Ž", 200)); err != nil {
		t.Fatal(err)
	}
	c, _, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil || utf8.RuneCountInString(c.Title) > 120 {
		t.Fatalf("title %d %v", utf8.RuneCountInString(c.Title), err)
	}
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id='w' AND user_id='u1'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenDirect(ctx, "w", "u0", "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, _, err := s.OpenDirect(ctx, "w", "u1", "u0"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
