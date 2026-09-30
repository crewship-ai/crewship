package access

import (
	"errors"
	"strings"
	"testing"
)

func groupFixture(t *testing.T) Store {
	t.Helper()
	s := fixture(t)
	for _, user := range []string{"h1", "h2"} {
		policy(t, s, user, Right{"agent", "a", "chat"})
	}
	if _, err := s.DB.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('group','w','a','h1','group'); INSERT INTO chat_participants(chat_id,user_id,role) VALUES('group','h2','member')`); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestExplicitGroupSharesOnlyItsCurrentScopedContext(t *testing.T) {
	s := groupFixture(t)
	for _, user := range []string{"h1", "h2"} {
		chat := "c1"
		if user == "h2" {
			chat = "c2"
		}
		h, _, err := s.AdmitChat(t.Context(), user, "w", "a", chat, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.AppendContext(t.Context(), h, ContextUser, user+"_PRIVATE_CANARY"); err != nil {
			t.Fatal(err)
		}
		if err = s.CompleteAttempt(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	h1, a1, err := s.AdmitChat(t.Context(), "h1", "w", "a", "group", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.AppendContext(t.Context(), h1, ContextUser, "GROUP_SHARED_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), h1); err != nil {
		t.Fatal(err)
	}
	h2, a2, err := s.AdmitChat(t.Context(), "h2", "w", "a", "group", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Scope != a2.Scope || a1.ContextAudience == "" {
		t.Fatal("group audience was not deliberately shared")
	}
	p, err := s.BuildContext(t.Context(), a2, "group turn2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Input, "GROUP_SHARED_CANARY") || strings.Contains(p.Input, "PRIVATE_CANARY") {
		t.Fatalf("group prompt leaked or missing %+v", p)
	}
	if _, err = s.DeriveContext(t.Context(), h2, ContextSummary, []string{e.ID}, "GROUP_SUMMARY"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"h1", "h2"} {
		entries, err := s.ContextEntriesForChat(t.Context(), user, "w", "a", "group")
		if err != nil {
			t.Fatal(err)
		}
		var content string
		for _, entry := range entries {
			content += entry.Content
		}
		if !strings.Contains(content, "GROUP_SHARED_CANARY") || !strings.Contains(content, "GROUP_SUMMARY") || strings.Contains(content, "PRIVATE_CANARY") {
			t.Fatalf("group projection %s", content)
		}
	}
	if err = s.RevokeAttempt(t.Context(), h1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), h2); !errors.Is(err, ErrDenied) {
		t.Fatalf("source revocation did not kill group consumer %v", err)
	}
}
func TestGroupMembershipRevisionAndParticipantEpochFailClosed(t *testing.T) {
	for _, stage := range []string{"permissions", "participants", "trusted", "missing", "run"} {
		t.Run(stage, func(t *testing.T) {
			s := groupFixture(t)
			h, a, err := s.AdmitChat(t.Context(), "h1", "w", "a", "group", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "permissions":
				policy(t, s, "h2")
				policy(t, s, "h2", Right{"agent", "a", "chat"})
			case "participants":
				if _, err = s.DB.Exec(`DELETE FROM chat_participants WHERE chat_id='group' AND user_id='h2';INSERT INTO chat_participants(chat_id,user_id,role) VALUES('group','h2','member')`); err != nil {
					t.Fatal(err)
				}
			case "trusted":
				m, err := s.Membership(t.Context(), "h2", "w")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Replace(t.Context(), "owner", "h2", "w", "trusted", m, nil); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if _, err = s.DB.Exec(`DELETE FROM workspace_members WHERE id='m2'`); err != nil {
					t.Fatal(err)
				}
			case "run":
				policy(t, s, "h1", Right{"agent", "a", "chat"}, Right{"agent", "a", "run"})
				if _, _, err = s.Admit(t.Context(), "h1", "w", "a", "group", "", nil); !errors.Is(err, ErrDenied) {
					t.Fatalf("run admitted group %v", err)
				}
				return
			}
			if _, err = s.Resolve(t.Context(), h); !errors.Is(err, ErrDenied) {
				t.Fatalf("old group attempt survived %v", err)
			}
			if _, err = s.BuildContext(t.Context(), a, "old"); !errors.Is(err, ErrDenied) {
				t.Fatalf("old group context survived %v", err)
			}
			if stage == "permissions" || stage == "participants" {
				_, fresh, err := s.AdmitChat(t.Context(), "h1", "w", "a", "group", "", nil)
				if err != nil {
					t.Fatal(err)
				}
				if fresh.Scope == a.Scope {
					t.Fatal("group regrant revived scope")
				}
			}
		})
	}
}

func TestSharedGroupDoesNotElevateSourceRights(t *testing.T) {
	s := groupFixture(t)
	policy(t, s, "h1", Right{"agent", "a", "chat"}, Right{"project", "p1", "read"})
	h, _, err := s.AdmitChat(t.Context(), "h1", "w", "a", "group", "", []Right{{"project", "p1", "read"}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.AppendContext(t.Context(), h, ContextUser, "GROUP_PROJECT_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	h2, a2, err := s.AdmitChat(t.Context(), "h2", "w", "a", "group", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.BuildContext(t.Context(), a2, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Input, "GROUP_PROJECT_SECRET") {
		t.Fatal("group audience elevated source rights")
	}
	if _, err = s.DeriveContext(t.Context(), h2, ContextSummary, []string{e.ID}, "launder"); !errors.Is(err, ErrDenied) {
		t.Fatalf("group source rights laundered %v", err)
	}
	if _, err = s.ContextEntriesForChat(t.Context(), "owner", "w", "a", "group"); !errors.Is(err, ErrDenied) {
		t.Fatalf("nonparticipant trusted operator read scoped group %v", err)
	}
}
