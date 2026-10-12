package access

import (
	"context"
	"errors"
	"testing"
)

func TestCompletedContextExactProjection(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"})
	handle, attempt, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.AppendContext(t.Context(), handle, ContextUser, "private question")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AppendContext(t.Context(), handle, ContextAssistant, "first answer")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendContext(t.Context(), handle, ContextAssistant, "second answer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompletedContext(t.Context(), handle, "h1", "w", "a", "c1", []string{first.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("active output exposed: %v", err)
	}
	if err := s.CompleteAttempt(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	rows, err := s.CompletedContext(t.Context(), handle, "h1", "w", "a", "c1", []string{second.ID, first.ID})
	if err != nil || len(rows) != 2 {
		t.Fatalf("projection: %#v, %v", rows, err)
	}
	if rows[0].Content != "second answer" || rows[1].Content != "first answer" {
		t.Fatalf("wrong output/order: %#v", rows)
	}
	for _, tc := range []struct {
		name, handle, user, workspace, agent, chat string
		ids                                        []string
	}{
		{"public attempt ID", attempt.ID, "h1", "w", "a", "c1", []string{first.ID}},
		{"unknown capability", "unknown", "h1", "w", "a", "c1", []string{first.ID}},
		{"other principal", handle, "h2", "w", "a", "c1", []string{first.ID}},
		{"other workspace", handle, "h1", "other", "a", "c1", []string{first.ID}},
		{"other agent", handle, "h1", "w", "b", "c1", []string{first.ID}},
		{"other conversation", handle, "h1", "w", "a", "c2", []string{first.ID}},
		{"duplicate output", handle, "h1", "w", "a", "c1", []string{first.ID, first.ID}},
		{"input is not output", handle, "h1", "w", "a", "c1", []string{user.ID}},
		{"unknown output", handle, "h1", "w", "a", "c1", []string{"missing"}},
		{"empty output", handle, "h1", "w", "a", "c1", nil},
		{"excessive output", handle, "h1", "w", "a", "c1", make([]string, 65)},
		{"empty capability", "", "h1", "w", "a", "c1", []string{first.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.CompletedContext(t.Context(), tc.handle, tc.user, tc.workspace, tc.agent, tc.chat, tc.ids)
			if !errors.Is(err, ErrDenied) || len(rows) != 0 {
				t.Fatalf("projection escaped binding: %#v, %v", rows, err)
			}
		})
	}
	if err := s.RevokeAttempt(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompletedContext(t.Context(), handle, "h1", "w", "a", "c1", []string{first.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked output exposed: %v", err)
	}
}

func TestCompletedContextUnavailableStorage(t *testing.T) {
	if _, err := (Store{}).CompletedContext(t.Context(), "handle", "h1", "w", "a", "c1", []string{"id"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil storage: %v", err)
	}
	s := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.CompletedContext(ctx, "handle", "h1", "w", "a", "c1", []string{"id"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}

func TestRestrictedMembershipGlobalBoundary(t *testing.T) {
	s := fixture(t)
	for _, user := range []string{"owner", "h1", "unknown"} {
		restricted, err := s.HasRestrictedMembership(t.Context(), user)
		if err != nil || restricted {
			t.Fatalf("trusted/absent %s: %v, %v", user, restricted, err)
		}
	}
	policy(t, s, "h1", Right{"agent", "a", "run"})
	restricted, err := s.HasRestrictedMembership(t.Context(), "h1")
	if err != nil || !restricted {
		t.Fatalf("restriction not detected: %v, %v", restricted, err)
	}
	if restricted, err = s.HasRestrictedMembership(t.Context(), ""); !restricted || !errors.Is(err, ErrDenied) {
		t.Fatalf("empty principal: %v, %v", restricted, err)
	}
	if restricted, err = (Store{}).HasRestrictedMembership(t.Context(), "h1"); !restricted || !errors.Is(err, ErrDenied) {
		t.Fatalf("no store: %v, %v", restricted, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.HasRestrictedMembership(ctx, "h1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
}

func TestProjectInputMetadataExactAuthority(t *testing.T) {
	s, one, two := projectFileFixture(t)
	if err := s.CheckProjectFile(t.Context(), "h1", "w", "p1", one.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckProjectFile(t.Context(), "h1", "w", "p2", two.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign file exposed: %v", err)
	}
	if err := (Store{}).CheckProjectFile(t.Context(), "h1", "w", "p1", one.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil store: %v", err)
	}
	rights, err := s.ProjectFileRights(t.Context(), "h1", "w", []string{one.ID})
	if err != nil {
		t.Fatal(err)
	}
	handle, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindProjectFileInputs(t.Context(), handle, []string{one.ID}); err != nil {
		t.Fatal(err)
	}
	versions, err := s.ProjectInputVersionsForAttempt(t.Context(), a)
	if err != nil || len(versions) != 1 || versions[0] != one {
		t.Fatalf("metadata differs: %#v, %v", versions, err)
	}
	for _, field := range []string{"scope", "generation", "principal", "workspace", "agent", "chat", "id"} {
		t.Run(field, func(t *testing.T) {
			forged := a
			switch field {
			case "scope":
				forged.Scope = "other"
			case "generation":
				forged.Generation++
			case "principal":
				forged.Principal = "h2"
			case "workspace":
				forged.Workspace = "other"
			case "agent":
				forged.Agent = "b"
			case "chat":
				forged.Chat = "c2"
			case "id":
				forged.ID = "missing"
			}
			if rows, err := s.ProjectInputVersionsForAttempt(t.Context(), forged); !errors.Is(err, ErrDenied) || len(rows) != 0 {
				t.Fatalf("forged metadata: %#v, %v", rows, err)
			}
		})
	}
	if _, err := (Store{}).ProjectInputVersionsForAttempt(t.Context(), a); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil store metadata: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ProjectInputVersionsForAttempt(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata: %v", err)
	}
	if err := s.CompleteAttempt(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectInputVersionsForAttempt(t.Context(), a); !errors.Is(err, ErrDenied) {
		t.Fatalf("completed execution still materializes: %v", err)
	}
}
