package pipeline

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newMetaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE pipeline_runs (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, pipeline_id TEXT, pipeline_slug TEXT,
    status TEXT, triggered_via TEXT, triggered_by_id TEXT, cost_usd REAL DEFAULT 0,
    started_at TEXT DEFAULT '', metadata_json TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT DEFAULT '');
INSERT INTO pipeline_runs (id, workspace_id, metadata_json) VALUES ('r1','w','{"count":2}');`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUpdateMetadata_SetIncrementAppend(t *testing.T) {
	db := newMetaDB(t)
	s := NewRunStore(db)
	ctx := context.Background()

	md, err := s.UpdateMetadata(ctx, "w", "r1", MetadataOps{
		Set:       map[string]any{"stage": "done"},
		Increment: map[string]any{"count": float64(3)}, // 2 + 3 = 5
		Append:    map[string]any{"errors": "boom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if md["stage"] != "done" {
		t.Errorf("set: %v", md["stage"])
	}
	if toFloat(md["count"]) != 5 {
		t.Errorf("increment: got %v want 5", md["count"])
	}
	if arr, ok := md["errors"].([]any); !ok || len(arr) != 1 || arr[0] != "boom" {
		t.Errorf("append: %v", md["errors"])
	}

	// Wrong workspace → not found.
	if _, err := s.UpdateMetadata(ctx, "other", "r1", MetadataOps{Set: map[string]any{"x": 1}}); err == nil {
		t.Fatal("cross-workspace update should fail")
	}
}

func TestRunTree_RootAndChildren(t *testing.T) {
	db := newMetaDB(t)
	// r1 is root; c1 child of r1; c2 child of c1.
	if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, status, triggered_by_id, pipeline_slug) VALUES
  ('c1','w','completed','r1','child'),
  ('c2','w','completed','c1','grandchild'),
  ('other','w','completed','zzz','unrelated');`); err != nil {
		t.Fatal(err)
	}
	s := NewRunStore(db)
	nodes, err := s.RunTree(context.Background(), "w", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("want 3 tree nodes (r1,c1,c2), got %d", len(nodes))
	}
	byID := map[string]RunTreeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if byID["r1"].ParentID != "" {
		t.Errorf("root parent should be blank, got %q", byID["r1"].ParentID)
	}
	if byID["c2"].ParentID != "c1" {
		t.Errorf("c2 parent: %q", byID["c2"].ParentID)
	}
}
