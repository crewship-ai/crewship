package access

import (
	"errors"
	"strings"
	"testing"
)

func TestDelegatedContextPreservesOriginAndCompletion(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}, {"project", "p1", "read"}}
	policy(t, s, "h1", rights...)
	policy(t, s, "h2", rights...)
	sourceHandle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendContext(t.Context(), sourceHandle, ContextAssistant, "A_PROJECT_SECRET_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), sourceHandle); err != nil {
		t.Fatal(err)
	}
	foreignHandle, _, err := s.Admit(t.Context(), "h2", "w", "a", "c2", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.AppendContext(t.Context(), foreignHandle, ContextAssistant, "H2_FOREIGN_CANARY")
	if err != nil {
		t.Fatal(err)
	}
	parentHandle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	childHandle, child, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parentHandle, rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeriveContext(t.Context(), childHandle, ContextSummary, []string{source.ID}, "launder"); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct cross-agent derive allowed %v", err)
	}
	before, err := s.BuildContext(t.Context(), child, "safe")
	if err != nil || strings.Contains(before.Input, "A_PROJECT_SECRET") {
		t.Fatalf("normal cross-agent context leaked %+v %v", before, err)
	}
	if _, err = s.ImportDelegatedContext(t.Context(), parentHandle, childHandle, []string{foreign.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign import %v", err)
	}
	imported, err := s.ImportDelegatedContext(t.Context(), parentHandle, childHandle, []string{source.ID})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := s.BuildContext(t.Context(), child, "safe")
	if err != nil || !strings.Contains(prompt.Input, "A_PROJECT_SECRET_CANARY") || strings.Contains(prompt.Input, "H2_FOREIGN_CANARY") {
		t.Fatalf("delegated prompt %+v %v", prompt, err)
	}
	if _, err = s.DB.ExecContext(t.Context(), `UPDATE access_context_delegations SET parent_attempt_id=? WHERE entry_id=?`, child.ID, imported.ID); err == nil {
		t.Fatal("delegation mutable")
	}
	if err = s.CompleteAttempt(t.Context(), childHandle); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), parentHandle); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckContextAttempt(t.Context(), childHandle); err != nil {
		t.Fatalf("completed child provenance %v", err)
	}
	if err = s.RevokeAttempt(t.Context(), sourceHandle); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckContextAttempt(t.Context(), childHandle); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked source did not revoke completed child %v", err)
	}
}

func TestDelegatedContextNarrowRightsCannotImportBroaderSource(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}, {"project", "p1", "read"}}
	policy(t, s, "h1", rights...)
	root, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendContext(t.Context(), root, ContextAssistant, "PROJECT_CLASSIFIED")
	if err != nil {
		t.Fatal(err)
	}
	narrow, _, err := s.Admit(t.Context(), "h1", "w", "b", "c1", root, []Right{{"agent", "b", "run"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportDelegatedContext(t.Context(), root, narrow, []string{source.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("narrow child imported broad source %v", err)
	}
	if _, _, err = s.Admit(t.Context(), "h1", "w", "a", "c1", narrow, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("narrow child escalated %v", err)
	}
}

func TestDelegatedContextSourceRevocationStopsTransitiveChild(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('c','w','crew','C','access-c','AGENT')`); err != nil {
		t.Fatal(err)
	}
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}, {"agent", "c", "run"}, {"agent", "c", "delegate"}}
	policy(t, s, "h1", rights...)
	parent, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.AppendContext(t.Context(), parent, ContextAssistant, "A_SOURCE")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parent, rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportDelegatedContext(t.Context(), parent, b, []string{source.ID}); err != nil {
		t.Fatal(err)
	}
	bSource, err := s.AppendContext(t.Context(), b, ContextAssistant, "B_DERIVED")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteAttempt(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	c, ca, err := s.Admit(t.Context(), "h1", "w", "c", "c1", parent, rights)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportDelegatedContext(t.Context(), parent, c, []string{bSource.ID})
	if err != nil {
		t.Fatal(err)
	}
	if prompt, err := s.BuildContext(t.Context(), ca, "safe"); err != nil || !strings.Contains(prompt.Input, "B_DERIVED") {
		t.Fatalf("transitive prompt %v %+v", err, prompt)
	}
	if _, err = s.DB.ExecContext(t.Context(), `DELETE FROM access_context_delegations WHERE entry_id=?`, imported.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), c); !errors.Is(err, ErrDenied) {
		t.Fatal("removed transfer provenance still executable")
	}
	if err = s.RevokeAttempt(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckContextAttempt(t.Context(), b); !errors.Is(err, ErrDenied) {
		t.Fatal("completed descendant survived source revoke")
	}
}
