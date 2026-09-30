package access

import (
	"errors"
	"strings"
	"testing"
)

func TestHumanMemoryRetainsProvenanceAndWithdrawsConsumers(t *testing.T) {
	s := fixture(t)
	for _, user := range []string{"h1", "h2"} {
		policy(t, s, user, Right{"agent", "a", "chat"})
	}
	note, err := s.SaveNote(t.Context(), "h1", "w", "a", "c1", "H1_MEMORY_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (Store{s.DB}).ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1")
	if err != nil || len(entries) != 2 {
		t.Fatalf("durable note sources: %+v %v", entries, err)
	}
	other, err := s.ContextEntriesForChat(t.Context(), "h2", "w", "a", "c2")
	if err != nil || len(other) != 0 {
		t.Fatalf("foreign memory: %+v %v", other, err)
	}
	if err = s.DeleteNote(t.Context(), "h2", "w", "a", "c2", note.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign delete: %v", err)
	}
	h, a, err := s.AdmitChat(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := s.BuildContext(t.Context(), a, "next turn")
	if err != nil || !strings.Contains(prompt.Input, "H1_MEMORY_CANARY") {
		t.Fatalf("memory recall: %+v %v", prompt, err)
	}
	if err = s.DeleteNote(t.Context(), "h1", "w", "a", "c1", note.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckContextAttempt(t.Context(), h); !errors.Is(err, ErrDenied) {
		t.Fatalf("withdrawn dependency survived: %v", err)
	}
	entries, err = s.ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1")
	if err != nil || len(entries) != 0 {
		t.Fatalf("withdrawn note visible: %+v %v", entries, err)
	}
}

func TestGroupMemoryRequiresCurrentAudienceAndOwnDeletion(t *testing.T) {
	s := groupFixture(t)
	note, err := s.SaveNote(t.Context(), "h1", "w", "a", "group", "GROUP_MEMORY_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.ContextEntriesForChat(t.Context(), "h2", "w", "a", "group")
	if err != nil || len(entries) != 2 {
		t.Fatalf("explicit shared note: %+v %v", entries, err)
	}
	if err = s.DeleteNote(t.Context(), "h2", "w", "a", "group", note.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("noncreator withdrew note: %v", err)
	}
	policy(t, s, "h1")
	if _, err = s.SaveNote(t.Context(), "h1", "w", "a", "group", "revoked"); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked memory write: %v", err)
	}
	if _, err = s.SaveNote(t.Context(), "h2", "w", "a", "group", "changed audience"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing group member authority: %v", err)
	}
}
