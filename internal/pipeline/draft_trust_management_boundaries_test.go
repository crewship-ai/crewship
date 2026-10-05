package pipeline

import (
	"errors"
	"testing"
)

func TestDraftDeletionRequiresExactWorkspaceIdentityAndRevision(t *testing.T) {
	s := draftStore(t)
	draft := saveTestDraft(t, s, "draft-delete")
	for _, tc := range []struct {
		ws, slug, id string
		revision     int
	}{
		{"other", draft.Slug, draft.ID, draft.Revision},
		{draft.WorkspaceID, "other", draft.ID, draft.Revision},
		{draft.WorkspaceID, draft.Slug, "other", draft.Revision},
		{draft.WorkspaceID, draft.Slug, draft.ID, draft.Revision - 1},
	} {
		if err := s.DeleteDraft(t.Context(), tc.ws, tc.slug, tc.id, tc.revision); !errors.Is(err, ErrDraftConflict) {
			t.Fatalf("stale deletion = %v", err)
		}
		saved, err := s.GetDraft(t.Context(), draft.WorkspaceID, draft.Slug)
		if err != nil || saved.ID != draft.ID || saved.Revision != draft.Revision {
			t.Fatalf("rejected deletion lost draft: %+v, %v", saved, err)
		}
	}
	if err := s.DeleteDraft(t.Context(), draft.WorkspaceID, draft.Slug, draft.ID, draft.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDraft(t.Context(), draft.WorkspaceID, draft.Slug, draft.ID, draft.Revision); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("repeated deletion = %v", err)
	}
	baseline, err := s.GetDraft(t.Context(), draft.WorkspaceID, draft.Slug)
	if err != nil || baseline.Revision != 0 {
		t.Fatalf("fresh draft baseline = %+v, %v", baseline, err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDraft(t.Context(), draft.WorkspaceID, draft.Slug, draft.ID, draft.Revision); err == nil {
		t.Fatal("storage refusal hidden")
	}
}

func TestPipelineTrustRevocationIsScopedAttributedAndIdempotent(t *testing.T) {
	db := openTrustGrantTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	s := NewTrustGrantStore(db)
	for _, tc := range []struct{ ws, pipeline, step string }{{"ws_test", "pl1", "publish"}, {"ws_test", "pl1", "notify"}, {"ws_test", "pl2", "publish"}, {"other", "pl1", "publish"}} {
		in := baseGrant()
		in.WorkspaceID = tc.ws
		in.PipelineID = tc.pipeline
		in.StepID = tc.step
		mustGrant(t, s, in)
	}
	if n, err := s.RevokeForPipeline(t.Context(), "ws_test", "pl1", "", "reason"); err == nil || n != 0 {
		t.Fatalf("unattributed revocation = %d, %v", n, err)
	}
	n, err := s.RevokeForPipeline(t.Context(), "ws_test", "pl1", "reviewer", "risk changed")
	if err != nil || n != 2 {
		t.Fatalf("revoked = %d, %v", n, err)
	}
	grants, err := s.List(t.Context(), "ws_test", "pl1")
	if err != nil || len(grants) != 2 {
		t.Fatalf("revocation history = %+v, %v", grants, err)
	}
	for _, g := range grants {
		if g.RevokedAt == nil || g.RevokedByUserID != "reviewer" || g.RevokeReason != "risk changed" {
			t.Fatalf("lost revocation provenance: %+v", g)
		}
	}
	if n, err := s.RevokeForPipeline(t.Context(), "ws_test", "pl1", "later-reviewer", "changed again"); err != nil || n != 0 {
		t.Fatalf("repeated revocation = %d, %v", n, err)
	}
	for _, tc := range []struct{ ws, pipeline string }{{"ws_test", "pl2"}, {"other", "pl1"}} {
		if _, ok, err := s.Consume(t.Context(), tc.ws, tc.pipeline, "publish", "hashA"); err != nil || !ok {
			t.Fatalf("unrelated trust revoked: %+v, %v", tc, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeForPipeline(t.Context(), "ws_test", "pl1", "reviewer", "reason"); err == nil {
		t.Fatal("storage refusal hidden")
	}
}

func TestTrustGrantInputRefusalsDoNotPersistAuthority(t *testing.T) {
	db := openTrustGrantTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	s := NewTrustGrantStore(db)
	zero := 0
	for _, change := range []func(*GrantInput){func(g *GrantInput) { g.WorkspaceID = "" }, func(g *GrantInput) { g.PipelineID = "" }, func(g *GrantInput) { g.StepID = "" }, func(g *GrantInput) { g.DefinitionHash = "" }, func(g *GrantInput) { g.GrantedByUserID = "" }, func(g *GrantInput) { g.MaxUses = &zero }} {
		in := baseGrant()
		change(&in)
		if _, err := s.Grant(t.Context(), in); err == nil {
			t.Fatal("invalid grant accepted")
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM waitpoint_trust_grants`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid input left %d grants: %v", count, err)
	}
}
