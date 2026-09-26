package groupchat

import (
	"context"
	"errors"
	"testing"
)

func TestChannelMembershipIndependentOfPublicAccessAndPreferences(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := context.Background()
	c := create(t, s, "channel")
	checkMembers := func(count int) {
		t.Helper()
		members, err := s.Members(ctx, "w", "u2", c.ID)
		if err != nil || len(members) != count {
			t.Fatalf("members=%v err=%v want=%d", members, err, count)
		}
	}
	checkMembers(1)
	if err := s.AddMember(ctx, "w", "u2", c.ID, "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner add: %v", err)
	}
	if err := s.AddMember(ctx, "w", "u0", c.ID, "u104"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider add: %v", err)
	}
	for range 2 {
		if err := s.AddMember(ctx, "w", "u0", c.ID, "u1"); err != nil {
			t.Fatal(err)
		}
	}
	checkMembers(2)
	if err := s.RemoveMember(ctx, "w", "u2", c.ID, "u1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner remove: %v", err)
	}
	if err := s.RemoveMember(ctx, "w", "u0", c.ID, "u0"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("owner removal: %v", err)
	}
	if err := s.SetMuted(ctx, "w", "u1", c.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, "w", "u0", c.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	checkMembers(1)
	for index, user := range []string{"u1", "u2"} {
		if _, _, err := s.Send(ctx, "w", user, c.ID, SendInput{ClientID: user, Content: "Public access remains"}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRead(ctx, "w", user, c.ID, int64(index+1)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetMuted(ctx, "w", user, c.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	checkMembers(1) // Reading, sending and muting do not silently rejoin.
	got, err := s.Get(ctx, "w", "u1", c.ID)
	if err != nil || !got.Muted || got.LastReadSequence == 0 {
		t.Fatalf("lost preferences: %+v %v", got, err)
	}
	if err := s.AddMember(ctx, "w", "u0", c.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	checkMembers(2)
}
