package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
)

func draftStore(t *testing.T) *Store {
	t.Helper()
	db := openVersioningTestDB(t)
	t.Cleanup(func() { db.Close() })
	sql, err := os.ReadFile("../database/migrations/20260908223757_pipeline_drafts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(sql)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(scheduleSchemaSQL); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}
func saveTestDraft(t *testing.T, s *Store, slug string) *Draft {
	t.Helper()
	d, err := s.GetDraft(context.Background(), "ws_test", slug)
	if err != nil {
		t.Fatal(err)
	}
	d.UpdatedBy = "user_test"
	d.Document = json.RawMessage(`{"slug":"` + slug + `","definition":{"name":"` + slug + `","steps":[]}}`)
	d, err = s.SaveDraft(context.Background(), *d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func publishInput(d *Draft) SaveInput {
	in := validSaveInput(d.Slug)
	in.Publication = &DraftPublication{ID: d.ID, Revision: d.Revision, Document: string(d.Document)}
	return in
}
func TestDraftDoesNotChangePublishedRecipe(t *testing.T) {
	s := draftStore(t)
	ctx := context.Background()
	old, err := s.Save(ctx, validSaveInput("draft-test"))
	if err != nil {
		t.Fatal(err)
	}
	d := saveTestDraft(t, s, old.Slug)
	current, _ := s.GetByID(ctx, old.ID)
	if current.DefinitionJSON != old.DefinitionJSON {
		t.Fatal("draft changed live recipe")
	}
	in := publishInput(d)
	in.DefinitionJSON = `{"name":"draft-test","steps":[{"id":"new","type":"transform","transform":{"input":"hello"}}]}`
	// The store consumes the handler-validated document identity. The handler
	// owns the conversion from that document into SaveInput and validates it.
	published, err := s.Save(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if published.DefinitionJSON != in.DefinitionJSON {
		t.Fatal("publication missing")
	}
	if readHeadVersion(t, s.db, old.ID) != 2 {
		t.Fatal("archive did not advance")
	}
	if _, err = s.Save(ctx, in); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("second publish: %v", err)
	}
	var archived string
	if err = s.db.QueryRow(`SELECT definition_json FROM pipeline_versions WHERE pipeline_id=? AND version=1`, old.ID).Scan(&archived); err != nil || archived != old.DefinitionJSON {
		t.Fatal("history rewritten", err)
	}
}

func TestN11RenameUnpublishedDraftKeepsIdentity(t *testing.T) {
	s := draftStore(t)
	d := saveTestDraft(t, s, "original")
	d.Slug = "renamed"
	d.Document = json.RawMessage(`{"slug":"renamed","definition":{"name":"renamed","steps":[]}}`)
	saved, err := s.SaveDraft(context.Background(), *d)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != d.ID || saved.Revision != d.Revision+1 {
		t.Fatalf("lost identity: %+v", saved)
	}
	old, err := s.GetDraft(context.Background(), "ws_test", "original")
	if err != nil || old.ID != "" {
		t.Fatalf("orphaned old draft: %+v %v", old, err)
	}
	if _, err := s.SaveDraft(context.Background(), *d); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("stale rename accepted: %v", err)
	}
}

func TestN11DraftRenameCannotReplaceOtherWork(t *testing.T) {
	s := draftStore(t)
	ctx := context.Background()
	d := saveTestDraft(t, s, "rename-source")
	other := saveTestDraft(t, s, "occupied")
	d.Slug = other.Slug
	if _, err := s.SaveDraft(ctx, *d); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("overwrote draft: %v", err)
	}
	p, err := s.Save(ctx, validSaveInput("published-target"))
	if err != nil {
		t.Fatal(err)
	}
	d.Slug = p.Slug
	if _, err := s.SaveDraft(ctx, *d); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("replaced published recipe: %v", err)
	}
	edit := saveTestDraft(t, s, p.Slug)
	edit.Slug = "fork-disallowed"
	if _, err := s.SaveDraft(ctx, *edit); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("renamed published edit: %v", err)
	}
}
func TestDraftConcurrentEditorsHaveOneWinner(t *testing.T) {
	s := draftStore(t)
	d := saveTestDraft(t, s, "new-draft")
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.SaveDraft(context.Background(), *d); out <- err }()
	}
	wg.Wait()
	close(out)
	successes, conflicts := 0, 0
	for err := range out {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrDraftConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	if _, err := s.GetBySlug(context.Background(), "ws_test", "new-draft"); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft created a live recipe")
	}
}
func TestDraftRejectsChangedBaseEvenAfterReturnToOriginalDefinition(t *testing.T) {
	s := draftStore(t)
	ctx := context.Background()
	in := validSaveInput("base-test")
	if _, err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	d := saveTestDraft(t, s, in.Slug)
	changed := in
	changed.DefinitionJSON = `{"name":"base-test","steps":[],"description":"changed"}`
	if _, err := s.Save(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, publishInput(d)); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("stale base accepted: %v", err)
	}
	retained, _ := s.GetDraft(ctx, "ws_test", in.Slug)
	if retained.ID != d.ID {
		t.Fatal("conflict discarded draft")
	}
}
func TestDraftPublicationChecksSchedulePresetsAtomically(t *testing.T) {
	s := draftStore(t)
	ctx := context.Background()
	p, err := s.Save(ctx, validSaveInput("planned"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json) VALUES('plan','ws_test','Daily',?,'0 9 * * *','{}')`, p.ID); err != nil {
		t.Fatal(err)
	}
	d := saveTestDraft(t, s, p.Slug)
	in := publishInput(d)
	in.DefinitionJSON = `{"name":"planned","inputs":[{"name":"new-field","type":"string","required":true}],"steps":[]}`
	if _, err = s.Save(ctx, in); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("broken plan accepted: %v", err)
	}
	var conflict *ScheduleDraftConflict
	if !errors.As(err, &conflict) || conflict.ScheduleID != "plan" || conflict.Name != "Daily" || conflict.Reason == "" {
		t.Fatalf("missing actionable schedule conflict: %v", err)
	}
	retained, _ := s.GetDraft(ctx, "ws_test", p.Slug)
	if retained.ID != d.ID {
		t.Fatal("failed publish lost draft")
	}
	current, _ := s.GetByID(ctx, p.ID)
	if current.DefinitionJSON != p.DefinitionJSON {
		t.Fatal("failed publish changed live recipe")
	}
	if _, err = s.db.Exec(`UPDATE pipeline_schedules SET target_pipeline_version=1 WHERE id='plan'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, in); err != nil {
		t.Fatal("pinned plan must retain old schema", err)
	}
}
func TestDraftBaseIgnoresRunCountersAndIsWorkspaceScoped(t *testing.T) {
	s := draftStore(t)
	ctx := context.Background()
	p, err := s.Save(ctx, validSaveInput("scope"))
	if err != nil {
		t.Fatal(err)
	}
	d := saveTestDraft(t, s, p.Slug)
	if _, err = s.db.Exec(`UPDATE pipelines SET invocation_count=invocation_count+1 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	other := publishInput(d)
	other.WorkspaceID = "other"
	if _, err = s.Save(ctx, other); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("foreign publish: %v", err)
	}
	if _, err = s.Save(ctx, publishInput(d)); err != nil {
		t.Fatal("run counters invalidated draft", err)
	}
}

func TestDraftUpdateCannotRewriteBaseMetadata(t *testing.T) {
	s := draftStore(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, validSaveInput("base-metadata")); err != nil {
		t.Fatal(err)
	}
	d := saveTestDraft(t, s, "base-metadata")
	original := *d
	d.BasePipelineID = "forged"
	d.BaseRevision = 900
	d.CreatedAt = "forged"
	saved, err := s.SaveDraft(ctx, *d)
	if err != nil {
		t.Fatal(err)
	}
	if saved.BasePipelineID != original.BasePipelineID || saved.BaseRevision != original.BaseRevision || saved.CreatedAt != original.CreatedAt {
		t.Fatalf("immutable metadata overwritten: %+v", saved)
	}
}
