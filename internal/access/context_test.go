package access

import (
	"errors"
	"strings"
	"testing"
)

func TestScopedContextTwoHumansAndDerivedExport(t *testing.T) {
	s := fixture(t)
	r := Right{"agent", "a", "run"}
	policy(t, s, "h1", r)
	policy(t, s, "h2", r)
	h1, a1, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, a2, err := s.Admit(t.Context(), "h2", "w", "a", "c2", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := s.AppendContext(t.Context(), h1, ContextUser, "H1_SECRET_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.AppendContext(t.Context(), h2, ContextUser, "H2_SECRET_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(t.Context(), `UPDATE access_context SET content='altered' WHERE id=?`, e1.ID); err == nil {
		t.Fatal("context version mutated in place")
	}
	summary, err := s.DeriveContext(t.Context(), h1, ContextSummary, []string{e1.ID}, "H1_SUMMARY_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DeriveContext(t.Context(), h1, ContextMemory, []string{summary.ID}, "H1_MEMORY_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeriveContext(t.Context(), h1, ContextSummary, []string{e2.ID}, "launder"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign provenance: %v", err)
	}
	recovered := Store{s.DB}
	for _, tc := range []struct {
		a       Attempt
		yes, no string
	}{{a1, "H1_SECRET_CANARY", "H2_SECRET_CANARY"}, {a2, "H2_SECRET_CANARY", "H1_SECRET_CANARY"}} {
		p, err := recovered.BuildContext(t.Context(), tc.a, "hello")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Input, tc.yes) || strings.Contains(p.Input, tc.no) || strings.Contains(p.System, tc.yes) {
			t.Fatalf("bad prompt %+v", p)
		}
		out, err := recovered.ContextEntries(t.Context(), tc.a)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range out {
			if strings.Contains(e.Content, tc.no) {
				t.Fatal("export leaked")
			}
		}
	}
	forged := a1
	forged.Scope = a2.Scope
	if _, err = s.BuildContext(t.Context(), forged, "hello"); !errors.Is(err, ErrDenied) {
		t.Fatalf("forged scope: %v", err)
	}
	if _, err = s.AppendContext(t.Context(), a1.ID, ContextUser, "public-id"); !errors.Is(err, ErrDenied) {
		t.Fatalf("ID capability: %v", err)
	}
	if _, err = s.AppendContext(t.Context(), h1, ContextRole("system"), "instructions"); !errors.Is(err, ErrDenied) {
		t.Fatalf("system ingestion: %v", err)
	}
}
func TestScopedContextRevocationAndRightsNarrowing(t *testing.T) {
	s := fixture(t)
	r := Right{"agent", "a", "run"}
	project := Right{"project", "p1", "read"}
	policy(t, s, "h1", r, project)
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", []Right{project})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.AppendContext(t.Context(), h, ContextAssistant, "PROJECT_SECRET_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeriveContext(t.Context(), h, ContextSummary, []string{e.ID}, "SUMMARY_SECRET_CANARY"); err != nil {
		t.Fatal(err)
	}
	narrow, na, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.BuildContext(t.Context(), na, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Input, "SECRET_CANARY") {
		t.Fatal("narrow attempt recalled broader resource")
	}
	_, sameRights, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", []Right{project})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.BuildContext(t.Context(), sameRights, "hello")
	if err != nil || !strings.Contains(before.Input, "SUMMARY_SECRET_CANARY") {
		t.Fatalf("missing positive derived context: %v", err)
	}
	if err = s.RevokeAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	_, err = s.BuildContext(t.Context(), sameRights, "hello")
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked derived context retained: %v", err)
	}
	if _, err = s.BuildContext(t.Context(), a, "hello"); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked admission: %v", err)
	}
	if _, err = s.AppendContext(t.Context(), h, ContextUser, "late"); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked append: %v", err)
	}
	if _, err = s.DeriveContext(t.Context(), narrow, ContextMemory, []string{e.ID}, "launder"); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked source: %v", err)
	}
	policy(t, s, "h1")
	policy(t, s, "h1", r, project)
	_, fresh, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", []Right{project})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.BuildContext(t.Context(), fresh, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Input, "SECRET_CANARY") {
		t.Fatal("regrant restored context")
	}
}

func TestCompletedAttemptRetainsHistoryWithoutExecutionReplay(t *testing.T) {
	s := fixture(t)
	run := Right{"agent", "a", "run"}
	chat := Right{"agent", "a", "chat"}
	policy(t, s, "h1", run, chat)
	policy(t, s, "h2", run, chat)
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", []Right{chat})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.AppendContext(t.Context(), h, ContextUser, "FIRST_TURN_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendContext(t.Context(), h, ContextAssistant, "FIRST_ANSWER_CANARY"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), h); !errors.Is(err, ErrDenied) {
		t.Fatalf("completed runtime replay: %v", err)
	}
	if _, err = s.BuildContext(t.Context(), a, "again"); !errors.Is(err, ErrDenied) {
		t.Fatalf("completed prompt replay: %v", err)
	}
	if _, err = s.AppendContext(t.Context(), h, ContextUser, "late"); !errors.Is(err, ErrDenied) {
		t.Fatalf("completed write: %v", err)
	}
	if _, _, err = s.Admit(t.Context(), "h1", "w", "a", "c1", h, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("completed delegation: %v", err)
	}
	if _, err = s.DB.Exec(`UPDATE access_attempts SET completed_at=NULL WHERE id=?`, a.ID); err == nil {
		t.Fatal("completed authority resurrected")
	}
	fresh, second, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", []Right{chat})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.BuildContext(t.Context(), second, "second")
	if err != nil || !strings.Contains(p.Input, "FIRST_ANSWER_CANARY") {
		t.Fatalf("history missing after completion: %v", err)
	}
	if _, err = s.DeriveContext(t.Context(), fresh, ContextSummary, []string{e.ID}, "COMPLETED_SUMMARY_CANARY"); err != nil {
		t.Fatal(err)
	}
	history, err := s.ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1")
	if err != nil || len(history) != 3 {
		t.Fatalf("read-only history: %d %v", len(history), err)
	}
	if _, err = s.ContextEntriesForChat(t.Context(), "h2", "w", "a", "c1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign projection: %v", err)
	}
	if err = s.RevokeAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	history, err = s.ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1")
	if err != nil || len(history) != 0 {
		t.Fatalf("revoked completed provenance retained: %d %v", len(history), err)
	}
}

func TestFrozenContextOriginRevokesConsumerAndDeletion(t *testing.T) {
	for _, mutation := range []string{"revoke", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			s := fixture(t)
			run := Right{"agent", "a", "run"}
			policy(t, s, "h1", run)
			policy(t, s, "h2", run)
			source, origin, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			e, err := s.AppendContext(t.Context(), source, ContextUser, "FROZEN_ORIGIN_CANARY")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.CompleteAttempt(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			consumer, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := s.Admit(t.Context(), "h2", "w", "a", "c2", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.BuildContext(t.Context(), a, "current")
			if err != nil || !strings.Contains(p.Input, "FROZEN_ORIGIN_CANARY") {
				t.Fatalf("positive context missing: %v", err)
			}
			var count int
			if err = s.DB.QueryRow(`SELECT COUNT(*) FROM access_context_dependencies WHERE attempt_id=? AND context_id=?`, a.ID, e.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("dependency not durable %d %v", count, err)
			}
			if mutation == "revoke" {
				err = s.RevokeAttempt(t.Context(), source)
			} else {
				_, err = s.DB.Exec(`DELETE FROM access_attempts WHERE id=?`, origin.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Resolve(t.Context(), consumer); !errors.Is(err, ErrDenied) {
				t.Fatalf("frozen consumer executable: %v", err)
			}
			if _, err = s.BuildContext(t.Context(), a, "replay"); !errors.Is(err, ErrDenied) {
				t.Fatalf("frozen consumer prompt replay: %v", err)
			}
			if _, err = s.Resolve(t.Context(), other); err != nil {
				t.Fatalf("unrelated principal revoked: %v", err)
			}
		})
	}
}
