package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// versions.go — SaveVersion validation + explicit-parent path,
// ListVersions limits + error paths, GetVersion / Rollback /
// getVersionByID miss paths, derefIntPtr.
// ---------------------------------------------------------------------------

func TestSaveVersion_InputValidation(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.SaveVersion(ctx, SaveVersionInput{DefinitionJSON: "{}"}); err == nil || !strings.Contains(err.Error(), "pipeline_id required") {
		t.Errorf("missing pipeline_id: %v", err)
	}
	if _, err := store.SaveVersion(ctx, SaveVersionInput{PipelineID: "p"}); err == nil || !strings.Contains(err.Error(), "definition_json required") {
		t.Errorf("missing definition_json: %v", err)
	}
}

func TestSaveVersion_ExplicitParentAndDefaultAuthorType(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	p, err := store.Save(ctx, validSaveInput("p-explicit-parent"))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Explicit ParentVersion + empty AuthorType (defaults to "agent").
	parent := 1
	v, err := store.SaveVersion(ctx, SaveVersionInput{
		PipelineID:     p.ID,
		DefinitionJSON: `{"name":"p-explicit-parent","steps":[{"id":"new"}]}`,
		ParentVersion:  &parent,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	if v.AuthorType != "agent" {
		t.Errorf("default author type: %q", v.AuthorType)
	}
	if v.ParentVersion == nil || *v.ParentVersion != 1 {
		t.Errorf("explicit parent: %v", v.ParentVersion)
	}
	if v.Version != 2 {
		t.Errorf("version: %d", v.Version)
	}
}

// TestVersions_MissingTable_ErrorPaths uses the base store schema
// (no pipeline_versions table) so the hash-lookup / list / get queries
// fail with a non-ErrNoRows error and the wrapped messages surface.
func TestVersions_MissingTable_ErrorPaths(t *testing.T) {
	db := openStoreTestDB(t) // intentionally no versioning schema
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.SaveVersion(ctx, SaveVersionInput{PipelineID: "p", DefinitionJSON: "{}"}); err == nil || !strings.Contains(err.Error(), "hash lookup") {
		t.Errorf("SaveVersion hash lookup: %v", err)
	}
	if _, err := store.ListVersions(ctx, "p", 10); err == nil || !strings.Contains(err.Error(), "ListVersions") {
		t.Errorf("ListVersions: %v", err)
	}
	if _, err := store.GetVersion(ctx, "p", 1); err == nil || !strings.Contains(err.Error(), "GetVersion") {
		t.Errorf("GetVersion: %v", err)
	}
	if _, err := store.getVersionByID(ctx, "x"); err == nil {
		t.Errorf("getVersionByID should error without table")
	}
}

func TestListVersions_Validation(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.ListVersions(ctx, "", 10); err == nil || !strings.Contains(err.Error(), "pipeline_id required") {
		t.Errorf("empty id: %v", err)
	}

	// Out-of-range limit normalises to the default and still works.
	p, err := store.Save(ctx, validSaveInput("p-limits"))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	vs, err := store.ListVersions(ctx, p.ID, 9999)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vs) != 1 {
		t.Errorf("expected the v1 row, got %d", len(vs))
	}
}

func TestGetVersionByID_NotFound(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)

	if _, err := store.getVersionByID(context.Background(), "plnv_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRollback_ErrorPaths(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	// Target version doesn't exist.
	if _, err := store.Rollback(ctx, "pln_missing", 1); err == nil || !strings.Contains(err.Error(), "load target") {
		t.Errorf("missing target: %v", err)
	}

	// Version row exists but the pipelines row doesn't (or is deleted):
	// UPDATE affects 0 rows → ErrNotFound.
	if _, err := db.Exec(`
INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id, created_at)
VALUES ('plnv_orphan', 'pln_orphan', 1, '{}', 'h', 'agent', 'a', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed orphan version: %v", err)
	}
	if _, err := store.Rollback(ctx, "pln_orphan", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("orphan rollback: %v", err)
	}
}

func TestDerefIntPtr(t *testing.T) {
	t.Parallel()
	if derefIntPtr(sql.NullInt64{}) != nil {
		t.Error("invalid NullInt64 should map to nil")
	}
	got := derefIntPtr(sql.NullInt64{Int64: 7, Valid: true})
	if got == nil || *got != 7 {
		t.Errorf("valid NullInt64: %v", got)
	}
}

func TestGenerateVersionID_Format(t *testing.T) {
	t.Parallel()
	id1 := generateVersionID()
	id2 := generateVersionID()
	if !strings.HasPrefix(id1, "plnv_c") {
		t.Errorf("prefix: %q", id1)
	}
	if id1 == id2 {
		t.Error("ids must be unique")
	}
}
