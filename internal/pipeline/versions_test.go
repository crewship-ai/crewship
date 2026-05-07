package pipeline

import (
	"context"
	"database/sql"
	"testing"
)

// versioningSchemaSQL extends the base store_test schema with the
// pipeline_versions table + head_version column. Mirrors v79
// migration; we don't pull in the full migrate package to keep
// these tests fast.
const versioningSchemaSQL = `
CREATE TABLE IF NOT EXISTS pipeline_versions (
    id              TEXT PRIMARY KEY,
    pipeline_id     TEXT NOT NULL,
    version         INTEGER NOT NULL,
    definition_json TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    author_type     TEXT NOT NULL,
    author_id       TEXT NOT NULL,
    parent_version  INTEGER,
    change_summary  TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    UNIQUE (pipeline_id, version),
    UNIQUE (pipeline_id, definition_hash)
);
ALTER TABLE pipelines ADD COLUMN head_version INTEGER NOT NULL DEFAULT 1;
`

func openVersioningTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.ExecContext(context.Background(), versioningSchemaSQL); err != nil {
		_ = db.Close()
		t.Fatalf("versioning schema: %v", err)
	}
	return db
}

func TestVersions_SaveCreatesV1(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	saved, err := store.Save(ctx, validSaveInput("p1"))
	if err != nil {
		t.Fatalf("save pipeline: %v", err)
	}
	v, err := store.SaveVersion(ctx, SaveVersionInput{
		PipelineID:     saved.ID,
		DefinitionJSON: saved.DefinitionJSON,
		AuthorType:     "agent",
		AuthorID:       "agent_lead",
		ChangeSummary:  "initial",
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	if v.Version != 1 {
		t.Errorf("first version: got %d", v.Version)
	}
	if v.ParentVersion != nil {
		t.Errorf("v1 should have no parent, got %v", *v.ParentVersion)
	}
}

func TestVersions_DuplicateHashIsIdempotent(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	saved, _ := store.Save(ctx, validSaveInput("p1"))

	v1, err := store.SaveVersion(ctx, SaveVersionInput{
		PipelineID: saved.ID, DefinitionJSON: saved.DefinitionJSON,
		AuthorType: "agent", AuthorID: "a1",
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := store.SaveVersion(ctx, SaveVersionInput{
		PipelineID: saved.ID, DefinitionJSON: saved.DefinitionJSON, // same hash
		AuthorType: "agent", AuthorID: "a2",
	})
	if err != nil {
		t.Fatalf("v2 (dup): %v", err)
	}
	if v1.ID != v2.ID {
		t.Errorf("dup hash should return same row, got %q vs %q", v1.ID, v2.ID)
	}
	if v2.Version != 1 {
		t.Errorf("dup hash should not bump version, got %d", v2.Version)
	}
}

func TestVersions_DifferentContentBumpsVersion(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	saved, _ := store.Save(ctx, validSaveInput("p1"))

	_, err := store.SaveVersion(ctx, SaveVersionInput{
		PipelineID: saved.ID, DefinitionJSON: `{"name":"v1","steps":[]}`,
		AuthorType: "agent", AuthorID: "a1",
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := store.SaveVersion(ctx, SaveVersionInput{
		PipelineID: saved.ID, DefinitionJSON: `{"name":"v2","steps":[]}`,
		AuthorType: "agent", AuthorID: "a1", ChangeSummary: "added step",
	})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2: got %d", v2.Version)
	}
	if v2.ParentVersion == nil || *v2.ParentVersion != 1 {
		t.Errorf("v2 parent should be 1, got %v", v2.ParentVersion)
	}
}

func TestVersions_ListReturnsNewestFirst(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	saved, _ := store.Save(ctx, validSaveInput("p1"))

	for i, json := range []string{`{"v":"1"}`, `{"v":"2"}`, `{"v":"3"}`} {
		_, err := store.SaveVersion(ctx, SaveVersionInput{
			PipelineID: saved.ID, DefinitionJSON: json,
			AuthorType: "agent", AuthorID: "a", ChangeSummary: string(rune('a' + i)),
		})
		if err != nil {
			t.Fatalf("save v%d: %v", i+1, err)
		}
	}
	out, err := store.ListVersions(ctx, saved.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("list len: got %d", len(out))
	}
	if out[0].Version != 3 || out[1].Version != 2 || out[2].Version != 1 {
		t.Errorf("expected newest first; got versions %d %d %d",
			out[0].Version, out[1].Version, out[2].Version)
	}
}

func TestVersions_Rollback(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	saved, _ := store.Save(ctx, validSaveInput("p1"))

	v1Json := `{"v":"1"}`
	v2Json := `{"v":"2"}`
	v3Json := `{"v":"3"}`
	_, _ = store.SaveVersion(ctx, SaveVersionInput{PipelineID: saved.ID, DefinitionJSON: v1Json, AuthorType: "agent", AuthorID: "a"})
	_, _ = store.SaveVersion(ctx, SaveVersionInput{PipelineID: saved.ID, DefinitionJSON: v2Json, AuthorType: "agent", AuthorID: "a"})
	_, _ = store.SaveVersion(ctx, SaveVersionInput{PipelineID: saved.ID, DefinitionJSON: v3Json, AuthorType: "agent", AuthorID: "a"})

	rolledBack, err := store.Rollback(ctx, saved.ID, 2)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rolledBack.DefinitionJSON != v2Json {
		t.Errorf("rolled-back DSL: got %q", rolledBack.DefinitionJSON)
	}
	// History should still have v3 (rollback doesn't delete)
	versions, _ := store.ListVersions(ctx, saved.ID, 0)
	if len(versions) != 3 {
		t.Errorf("rollback should preserve history; got %d versions", len(versions))
	}
}

func TestVersions_GetVersion_NotFound(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	saved, _ := store.Save(context.Background(), validSaveInput("p1"))
	_, err := store.GetVersion(context.Background(), saved.ID, 99)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
