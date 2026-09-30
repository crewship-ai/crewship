package access

import (
	"errors"
	"slices"
	"testing"
)

func TestAdmissionOperationsRemainDistinctAndImmutable(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "chat"})
	chat, attempt, err := s.AdmitChat(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.AdmissionOperation != "chat" || slices.Contains(attempt.Rights, Right{"agent", "a", "run"}) {
		t.Fatalf("chat authority %+v", attempt)
	}
	if _, _, err = s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("chat widened to run %v", err)
	}
	if _, err = s.DB.ExecContext(t.Context(), `UPDATE access_attempts SET admission_operation='run' WHERE id=?`, attempt.ID); err == nil {
		t.Fatal("durable operation changed")
	}
	if _, err = s.Resolve(t.Context(), chat); err != nil {
		t.Fatal(err)
	}
	policy(t, s, "h1", Right{"agent", "a", "run"})
	if _, _, err = s.AdmitChat(t.Context(), "h1", "w", "a", "c1", "", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("run widened to chat %v", err)
	}
	handle, attempt, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.AdmissionOperation != "run" || slices.Contains(attempt.Rights, Right{"agent", "a", "chat"}) {
		t.Fatalf("run authority %+v", attempt)
	}
	fresh, err := s.Resolve(t.Context(), handle)
	if err != nil || fresh.AdmissionOperation != "run" {
		t.Fatalf("recovered run %+v %v", fresh, err)
	}
}
func TestChatDelegationCannotAcquireRunFromTargetsBroaderGrants(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "chat"}, {"agent", "a", "run"}, {"agent", "b", "chat"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}}
	policy(t, s, "h1", rights...)
	parent, _, err := s.AdmitChat(t.Context(), "h1", "w", "a", "c1", "", []Right{rights[2], rights[4]})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Admit(t.Context(), "h1", "w", "b", "c1", parent, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("child run escalation %v", err)
	}
	child, attempt, err := s.AdmitChat(t.Context(), "h1", "w", "b", "c1", parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.AdmissionOperation != "chat" || slices.Contains(attempt.Rights, rights[3]) {
		t.Fatalf("child scope %+v", attempt)
	}
	if _, err = s.Resolve(t.Context(), child); err != nil {
		t.Fatal(err)
	}
}
