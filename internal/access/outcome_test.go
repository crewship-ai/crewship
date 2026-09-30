package access

import (
	"errors"
	"testing"
)

func TestOutcomeOwnAuditSurvivesExecutionRevocation(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"}, Right{"agent", "a", "chat"})
	policy(t, s, "h2", Right{"agent", "a", "run"}, Right{"agent", "a", "chat"})
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordOutcome(t.Context(), a.ID, "failed"); !errors.Is(err, ErrDenied) {
		t.Fatalf("public ID accepted: %v", err)
	}
	if err = s.RecordOutcome(t.Context(), h, "failed"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordOutcome(t.Context(), h, "failed"); err != nil {
		t.Fatal("idempotence", err)
	}
	if err = s.RecordOutcome(t.Context(), h, "completed"); !errors.Is(err, ErrDenied) {
		t.Fatal("failure rewritten", err)
	}
	if _, err = s.DB.Exec(`UPDATE access_attempt_outcomes SET state='completed'`); err == nil {
		t.Fatal("immutable outcome rewritten")
	}
	out, err := s.OutcomesForChat(t.Context(), "h1", "w", "c1")
	if err != nil || len(out) != 1 || out[0].State != "failed" || out[0].RecordedAt == "" {
		t.Fatalf("own failure unavailable: %+v %v", out, err)
	}
	if _, err = s.OutcomesForChat(t.Context(), "h2", "w", "c1"); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign audit leaked", err)
	}
	policy(t, s, "h1", Right{"agent", "a", "chat"})
	out, err = s.OutcomesForChat(t.Context(), "h1", "w", "c1")
	if err != nil || len(out) != 1 {
		t.Fatal("revoked run erased content-free audit", err)
	}
	policy(t, s, "h1")
	if _, err = s.OutcomesForChat(t.Context(), "h1", "w", "c1"); !errors.Is(err, ErrDenied) {
		t.Fatal("current chat grant bypassed", err)
	}
}
