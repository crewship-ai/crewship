package groupchat

import (
	"context"
	"errors"
	"testing"
)

func TestHumanIdentityUsesCurrentProfileAcrossSendRetryHistoryAndRoster(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "group", "u1")
	if _, err := db.Exec(`UPDATE users SET full_name='Pavel',avatar_url='/api/v1/users/u1/avatar?v=1' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	in := SendInput{ClientID: "identity", Content: "Hello"}
	m, _, err := s.Send(ctx, "w", "u1", c.ID, in)
	if err != nil || m.AuthorName != "Pavel" || m.AuthorAvatarURL != "/api/v1/users/u1/avatar?v=1" {
		t.Fatalf("send identity: %+v %v", m, err)
	}
	if _, err := db.Exec(`UPDATE users SET full_name='Pavel Updated',avatar_url='/api/v1/users/u1/avatar?v=2' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	retry, duplicate, err := s.Send(ctx, "w", "u1", c.ID, in)
	if err != nil || !duplicate || retry.AuthorName != "Pavel Updated" || retry.AuthorAvatarURL != "/api/v1/users/u1/avatar?v=2" {
		t.Fatalf("retry identity: %+v %v", retry, err)
	}
	for _, backwards := range []bool{false, true} {
		var messages []Message
		if backwards {
			messages, err = s.MessagesBefore(ctx, "w", "u0", c.ID, 0, 20)
		} else {
			messages, err = s.Messages(ctx, "w", "u0", c.ID, 0, 20)
		}
		if err != nil || len(messages) != 1 || messages[0].AuthorAvatarURL != retry.AuthorAvatarURL {
			t.Fatalf("history %v: %+v %v", backwards, messages, err)
		}
	}
	members, err := s.Members(ctx, "w", "u0", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.UserID == "u1" && (m.Name != "Pavel Updated" || m.AvatarURL != retry.AuthorAvatarURL) {
			t.Fatalf("member %+v", m)
		}
	}
	if _, err = s.Messages(ctx, "w", "u2", c.ID, 0, 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("private history access: %v", err)
	}
}

func TestDirectIdentityIsCallerRelativeFreshAndDoesNotLeakRemovedPeer(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE users SET avatar_url='/api/v1/users/'||id||'/avatar' WHERE id IN ('u0','u1')`); err != nil {
		t.Fatal(err)
	}
	c, created, err := s.OpenDirect(ctx, "w", "u0", "u1")
	if err != nil || !created || c.DirectUserID != "u1" || c.DirectUserName != "User u1" || c.DirectAvatarURL != "/api/v1/users/u1/avatar" {
		t.Fatalf("new direct %+v %v", c, err)
	}
	other, created, err := s.OpenDirect(ctx, "w", "u1", "u0")
	if err != nil || created || other.ID != c.ID || other.DirectUserID != "u0" {
		t.Fatalf("reverse direct %+v %v", other, err)
	}
	if _, err = db.Exec(`UPDATE users SET full_name='Updated Peer',avatar_url=NULL WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "w", "u0", 10)
	if err != nil || len(list) != 1 || list[0].DirectUserName != "Updated Peer" || list[0].DirectAvatarURL != "" {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err = db.Exec(`DELETE FROM workspace_members WHERE workspace_id='w' AND user_id='u1'`); err != nil {
		t.Fatal(err)
	}
	c, err = s.Get(ctx, "w", "u0", c.ID)
	if err != nil || c.DirectUserID != "" || c.DirectAvatarURL != "" {
		t.Fatalf("removed peer %+v %v", c, err)
	}
}

func TestAgentIdentityReusesStoredRenderSeedAndCrewStyle(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	if _, err := db.Exec(`UPDATE crews SET avatar_style='avataaars' WHERE id='crew'; INSERT INTO agents(id,workspace_id,name,slug,avatar_seed,avatar_svg_hash) VALUES('a','w','Ava','ava','custom-seed','hash+1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, "w", "u0", c.ID, "a"); err != nil {
		t.Fatal(err)
	}
	agents, err := s.Agents(ctx, "w", "u0", c.ID)
	want := "/api/v1/agents/a/avatar?v=hash%2B1&workspace_id=w"
	if err != nil || len(agents) != 1 || agents[0].AvatarURL != want || agents[0].AvatarStyle != "avataaars" || agents[0].AvatarSeed != "custom-seed" {
		t.Fatalf("agents %+v %v", agents, err)
	}
	if err := s.write(ctx, func(q querier) error {
		return appendEventMessage(ctx, q, Message{ID: "reply", ConversationID: c.ID, Sequence: 2, AuthorAgentID: "a", Kind: "message", ClientID: "reply", Content: "Ready", CreatedAt: now()})
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Messages(ctx, "w", "u0", c.ID, 1, 20)
	if err != nil || len(messages) != 1 || messages[0].AuthorAvatarURL != want || messages[0].AuthorAvatarStyle != "avataaars" || messages[0].AuthorAvatarSeed != "custom-seed" || messages[0].AuthorSlug != "ava" {
		t.Fatalf("agent message %+v %v", messages, err)
	}
	if _, err = db.Exec(`UPDATE agents SET avatar_style='bottts',avatar_seed=NULL,avatar_svg_hash=NULL WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	messages, err = s.MessagesBefore(ctx, "w", "u0", c.ID, 0, 20)
	if err != nil || len(messages) != 2 || messages[1].AuthorAvatarURL != "" || messages[1].AuthorAvatarStyle != "bottts" || messages[1].AuthorAvatarSeed != "ava" {
		t.Fatalf("updated identity %+v %v", messages, err)
	}
}
