package groupchat

import (
	"errors"
	"sync"
	"testing"
)

func TestContinueDirectFreshHistoryCanonicalPairAndConcurrentRetry(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := t.Context()
	dm, _, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, "w", "u0", dm.ID, SendInput{ClientID: "private", Content: "PRIVATE DM HISTORY"}); err != nil {
		t.Fatal(err)
	}
	in := ContinueInput{Kind: "group", Title: "Planning", MemberIDs: []string{"u2", "u2"}, ClientID: "continue-one"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[string]bool{}
	createdCount := 0
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			room, created, err := s.Continue(ctx, "w", "u1", dm.ID, in)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[room.ID] = true
			if created {
				createdCount++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 1 || createdCount != 1 {
		t.Fatalf("retry created %d rooms (%v)", createdCount, ids)
	}
	var id string
	for id = range ids {
	}
	room, err := s.Get(ctx, "w", "u2", id)
	if err != nil || room.IsDirect || room.LastSequence != 0 || room.CreatedBy != "u1" {
		t.Fatalf("new group %#v %v", room, err)
	}
	members, err := s.Members(ctx, "w", "u2", id)
	if err != nil || len(members) != 3 {
		t.Fatalf("members %#v %v", members, err)
	}
	history, err := s.Messages(ctx, "w", "u2", id, 0, 100)
	if err != nil || len(history) != 0 {
		t.Fatalf("copied private history %#v %v", history, err)
	}
	if _, err = s.Get(ctx, "w", "u2", dm.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("new participant can read original DM")
	}
	reopened, newDM, err := s.OpenDirect(ctx, "w", "u1", "u0")
	if err != nil || newDM || reopened.ID != dm.ID || reopened.LastSequence != 1 {
		t.Fatalf("original pair changed %#v %v", reopened, err)
	}
	in.Title = "Changed"
	if _, _, err = s.Continue(ctx, "w", "u1", dm.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_continuations`).Scan(&n); err != nil || n != 1 {
		t.Fatal("bad durable retry map")
	}
}
func TestContinueChannelAtomicAgentNoPrivateHistoryOrJobs(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := t.Context()
	dm, _, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Send(ctx, "w", "u0", dm.ID, SendInput{ClientID: "private", Content: "PRIVATE"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO agents(id,workspace_id,name) VALUES('continue-agent','w','Ava')`); err != nil {
		t.Fatal(err)
	}
	in := ContinueInput{Kind: "channel", Title: "Workspace discussion", AgentID: "continue-agent", ClientID: "channel-one"}
	channel, created, err := s.Continue(ctx, "w", "u1", dm.ID, in)
	if err != nil || !created || channel.Kind != "channel" {
		t.Fatalf("channel %#v %v", channel, err)
	}
	roster, err := s.Agents(ctx, "w", "u3", channel.ID)
	if err != nil || len(roster) != 1 {
		t.Fatalf("roster %#v %v", roster, err)
	}
	messages, err := s.Messages(ctx, "w", "u3", channel.ID, 0, 100)
	if err != nil || len(messages) != 1 || messages[0].Kind != "agent_joined" {
		t.Fatalf("history %#v %v", messages, err)
	}
	var jobs int
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("joining ran agent")
	}
	in.ClientID = "rollback"
	if _, err = db.Exec(`CREATE TRIGGER reject_continuation BEFORE INSERT ON workspace_conversation_continuations BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Continue(ctx, "w", "u1", dm.ID, in); err == nil {
		t.Fatal("expected rollback")
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM workspace_conversations`).Scan(&count); err != nil || count != 2 {
		t.Fatal("partial channel survived rollback")
	}
}
func TestContinueACLAndValidation(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := t.Context()
	dm, _, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil {
		t.Fatal(err)
	}
	base := ContinueInput{Kind: "group", Title: "Team", MemberIDs: []string{"u2"}, ClientID: "retry"}
	for _, tc := range []struct {
		name, user, w string
		input         ContinueInput
		want          error
	}{
		{"outsider", "u3", "w", base, ErrForbidden},
		{"foreign workspace", "u104", "other", base, ErrForbidden},
		{"foreign invite", "u0", "w", ContinueInput{Kind: "group", Title: "Team", MemberIDs: []string{"u104"}, ClientID: "foreign"}, ErrForbidden},
		{"no new person", "u0", "w", ContinueInput{Kind: "group", Title: "Team", MemberIDs: []string{"u1"}, ClientID: "none"}, ErrInvalid},
		{"private agent", "u0", "w", ContinueInput{Kind: "group", Title: "Team", MemberIDs: []string{"u2"}, AgentID: "a", ClientID: "agent"}, ErrInvalid},
		{"missing retry id", "u0", "w", ContinueInput{Kind: "group", Title: "Team", MemberIDs: []string{"u2"}}, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.Continue(ctx, tc.w, tc.user, dm.ID, tc.input); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
