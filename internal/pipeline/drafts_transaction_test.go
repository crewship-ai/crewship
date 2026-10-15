package pipeline

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDraftInSharesCallerCommitAndRollback(t *testing.T) {
	s := draftStore(t)
	ctx := t.Context()
	published, err := s.Save(ctx, validSaveInput("atomic-draft"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TABLE draft_audit_sentinel(value INTEGER CHECK(value=0))`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	baseline, err := s.GetDraftIn(ctx, tx, "ws_test", published.Slug)
	if err != nil || baseline.BasePipelineID != published.ID || baseline.Revision != 0 {
		t.Fatal("wrong published baseline", baseline, err)
	}
	baseline.UpdatedBy = "user_test"
	baseline.Document = json.RawMessage(`{"slug":"atomic-draft","definition":{"name":"draft candidate","steps":[]}}`)
	saved, err := s.SaveDraftIn(ctx, tx, *baseline)
	if err != nil {
		t.Fatal(err)
	}
	within, err := s.GetDraftIn(ctx, tx, "ws_test", published.Slug)
	if err != nil || within.ID != saved.ID || within.Revision != 1 {
		t.Fatal("write escaped caller snapshot", within, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO draft_audit_sentinel VALUES(1)`); err == nil {
		t.Fatal("synthetic audit failure did not fire")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetDraft(ctx, "ws_test", published.Slug)
	if err != nil || after.ID != "" || after.Revision != 0 {
		t.Fatal("failed caller audit committed draft", after, err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	saved, err = s.SaveDraftIn(ctx, tx, *baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal("store stole caller transaction", err)
	}
	after, err = s.GetDraft(ctx, "ws_test", published.Slug)
	if err != nil || after.ID != saved.ID || after.Revision != 1 {
		t.Fatal("caller commit missing", after, err)
	}
	var versions int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id=?`, published.ID).Scan(&versions); err != nil || versions != 1 {
		t.Fatal("draft changed published version history", versions, err)
	}
	current, err := s.GetByID(ctx, published.ID)
	if err != nil || current.DefinitionJSON != published.DefinitionJSON || current.DefinitionHash != published.DefinitionHash {
		t.Fatal("draft changed executable recipe", current, err)
	}
}

func TestDraftInPreservesRevisionCASAndCallerRollback(t *testing.T) {
	s := draftStore(t)
	ctx := t.Context()
	original := saveTestDraft(t, s, "collaborative-draft")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	edit := *original
	edit.Document = json.RawMessage(`{"slug":"collaborative-draft","definition":{"steps":[],"description":"unsaved edit"}}`)
	saved, err := s.SaveDraftIn(ctx, tx, edit)
	if err != nil || saved.ID != original.ID || saved.Revision != original.Revision+1 {
		t.Fatal("existing draft lost identity or revision", saved, err)
	}
	if _, err = s.SaveDraftIn(ctx, tx, edit); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("stale snapshot overwrote an edit: %v", err)
	}
	foreign := *saved
	foreign.WorkspaceID = "another-workspace"
	if _, err = s.SaveDraftIn(ctx, tx, foreign); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("draft ID crossed workspace boundary: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetDraft(ctx, original.WorkspaceID, original.Slug)
	if err != nil || after.Revision != original.Revision || string(after.Document) != string(original.Document) {
		t.Fatal("caller rollback lost the original collaborative draft", after, err)
	}
}
