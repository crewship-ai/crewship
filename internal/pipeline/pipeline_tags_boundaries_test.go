package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func tagBoundaryStore(t *testing.T) (*PipelineTagStore, *sql.DB) {
	t.Helper()
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	_, err := db.Exec(`CREATE TABLE pipeline_tags (pipeline_id TEXT NOT NULL, workspace_id TEXT NOT NULL, tag TEXT NOT NULL, PRIMARY KEY(pipeline_id, tag))`)
	if err != nil {
		t.Fatal(err)
	}
	return NewPipelineTagStore(db), db
}

func TestPipelineTagsNormalizeDiscoverAndRemove(t *testing.T) {
	s, _ := tagBoundaryStore(t)
	ctx := context.Background()
	for _, row := range []struct {
		ws, id string
		tags   []string
	}{
		{"a", "one", []string{" Billing ", "finance", "BILLING", " "}},
		{"a", "two", []string{"billing"}},
		{"b", "three", []string{"billing"}},
	} {
		if err := s.Add(ctx, row.ws, row.id, row.tags); err != nil {
			t.Fatal(err)
		}
	}
	tags, err := s.TagsFor(ctx, "one")
	if err != nil || !reflect.DeepEqual(tags, []string{"billing", "finance"}) {
		t.Fatalf("tags = %v, %v", tags, err)
	}
	ids, err := s.PipelineIDsByTag(ctx, "a", " BILLING ")
	if err != nil || !reflect.DeepEqual(ids, map[string]struct{}{"one": {}, "two": {}}) {
		t.Fatalf("discovery = %v, %v", ids, err)
	}
	if err := s.Remove(ctx, "one", " BILLING "); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "one", "missing"); err != nil {
		t.Fatal(err)
	}
	tags, err = s.TagsFor(ctx, "one")
	if err != nil || !reflect.DeepEqual(tags, []string{"finance"}) {
		t.Fatalf("remaining = %v, %v", tags, err)
	}
	ids, err = s.PipelineIDsByTag(ctx, "missing", "billing")
	if err != nil || len(ids) != 0 {
		t.Fatalf("missing workspace = %v, %v", ids, err)
	}
}

func TestPipelineTagsRejectWholeOverLimitBatch(t *testing.T) {
	s, _ := tagBoundaryStore(t)
	ctx := context.Background()
	initial := make([]string, MaxPipelineTags-1)
	for i := range initial {
		initial[i] = fmt.Sprintf("tag-%02d", i)
	}
	if err := s.Add(ctx, "a", "one", initial); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, "a", "one", []string{"new-a", "new-b"}); !errors.Is(err, ErrTooManyTags) {
		t.Fatalf("over-limit error = %v", err)
	}
	tags, err := s.TagsFor(ctx, "one")
	if err != nil || !reflect.DeepEqual(tags, initial) {
		t.Fatalf("rejected batch changed tags: %v, %v", tags, err)
	}
}

func TestPipelineTagsDuplicatesRemainIdempotentAtLimit(t *testing.T) {
	s, _ := tagBoundaryStore(t)
	ctx := context.Background()
	initial := make([]string, MaxPipelineTags)
	for i := range initial {
		initial[i] = fmt.Sprintf("tag-%02d", i)
	}
	if err := s.Add(ctx, "a", "one", initial); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, "a", "one", []string{" TAG-00 ", "tag-19", " "}); err != nil {
		t.Fatalf("repeated labels at capacity: %v", err)
	}
	tags, err := s.TagsFor(ctx, "one")
	if err != nil || !reflect.DeepEqual(tags, initial) {
		t.Fatalf("tags changed: %v, %v", tags, err)
	}
}

func TestPipelineTagsStorageFailureRollsBackBatch(t *testing.T) {
	s, db := tagBoundaryStore(t)
	if _, err := db.Exec(`CREATE TRIGGER refuse_tag BEFORE INSERT ON pipeline_tags WHEN NEW.tag='refused' BEGIN SELECT RAISE(ABORT,'tag storage refused'); END`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Add(ctx, "a", "one", []string{"first", "refused"}); err == nil {
		t.Fatal("storage failure ignored")
	}
	tags, err := s.TagsFor(ctx, "one")
	if err != nil || len(tags) != 0 {
		t.Fatalf("partial write: %v, %v", tags, err)
	}
}

func TestPipelineTagStorageErrorsReachCaller(t *testing.T) {
	s, db := tagBoundaryStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Add(ctx, "a", "one", []string{"tag"}); err == nil {
		t.Error("Add ignored closed storage")
	}
	if err := s.Remove(ctx, "one", "tag"); err == nil {
		t.Error("Remove ignored closed storage")
	}
	if _, err := s.TagsFor(ctx, "one"); err == nil {
		t.Error("TagsFor ignored closed storage")
	}
	if _, err := s.PipelineIDsByTag(ctx, "a", "tag"); err == nil {
		t.Error("discovery ignored closed storage")
	}
}
