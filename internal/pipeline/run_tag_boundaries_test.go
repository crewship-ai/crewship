package pipeline

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRunTagReplacementNormalizationAndCap(t *testing.T) {
	s, db := openRunsTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedRunRow(t, s, "one", "pln_a", RunStatusCompleted)
	ctx := t.Context()
	if err := s.SetTags(ctx, "ws_runs", "one", []string{" Billing ", "billing", " ", "finance"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TagsFor(ctx, "one")
	if err != nil || !reflect.DeepEqual(got, []string{"billing", "finance"}) {
		t.Fatalf("normalized tags = %v, %v", got, err)
	}
	tags := []string{strings.Repeat("X", 140)}
	for i := 0; i < MaxRunTags+2; i++ {
		tags = append(tags, fmt.Sprintf("tag-%02d", i))
	}
	if err := s.SetTags(ctx, "ws_runs", "one", tags); err != nil {
		t.Fatal(err)
	}
	got, err = s.TagsFor(ctx, "one")
	if err != nil || len(got) != MaxRunTags || got[len(got)-1] != strings.Repeat("x", 128) {
		t.Fatalf("bounded replacement = %v, %v", got, err)
	}
	if err := s.SetTags(ctx, "ws_runs", "one", nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.TagsFor(ctx, "one")
	if err != nil || len(got) != 0 {
		t.Fatalf("clear = %v, %v", got, err)
	}
}

func TestRunTagReplacementFailurePreservesOriginalLabels(t *testing.T) {
	for _, operation := range []string{"INSERT", "DELETE"} {
		t.Run(operation, func(t *testing.T) {
			s, db := openRunsTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			if err := s.SetTags(t.Context(), "ws_runs", "one", []string{"original"}); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER refuse_tag BEFORE DELETE ON run_tags BEGIN SELECT RAISE(ABORT,'refused delete'); END`
			if operation == "INSERT" {
				trigger = `CREATE TRIGGER refuse_tag BEFORE INSERT ON run_tags WHEN NEW.tag='refused' BEGIN SELECT RAISE(ABORT,'refused insert'); END`
			}
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if err := s.SetTags(t.Context(), "ws_runs", "one", []string{"first", "refused"}); err == nil {
				t.Fatal("write failure ignored")
			}
			got, err := s.TagsFor(t.Context(), "one")
			if err != nil || !reflect.DeepEqual(got, []string{"original"}) {
				t.Fatalf("failed replacement changed tags: %v, %v", got, err)
			}
		})
	}
}

func TestRunTagDiscoveryIsOrderedBoundedAndPipelineScoped(t *testing.T) {
	s, db := openRunsTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	for i, id := range []string{"old", "new", "other"} {
		pipeline := "pln_a"
		if id == "other" {
			pipeline = "pln_b"
		}
		seedRunRow(t, s, id, pipeline, RunStatusCompleted)
		if _, err := db.Exec(`UPDATE pipeline_runs SET started_at=? WHERE id=?`, fmt.Sprintf("2026-10-03T12:00:0%dZ", i), id); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTags(t.Context(), "ws_runs", id, []string{"batch"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, 501, 1, 2} {
		rows, err := s.ListByTag(t.Context(), "pln_a", " BATCH ", limit)
		want := 2
		if limit == 1 {
			want = 1
		}
		if err != nil || len(rows) != want {
			t.Fatalf("limit %d = %v, %v", limit, rows, err)
		}
		if rows[0].ID != "new" || (want == 2 && rows[1].ID != "old") {
			t.Fatalf("wrong order or pipeline: %+v", rows)
		}
	}
	rows, err := s.ListByTag(t.Context(), "pln_a", "missing", 50)
	if err != nil || len(rows) != 0 {
		t.Fatalf("missing tag = %v, %v", rows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTags(t.Context(), "ws_runs", "one", nil); err == nil {
		t.Error("closed DB accepted tags")
	}
	if _, err := s.TagsFor(t.Context(), "one"); err == nil {
		t.Error("closed DB returned tags")
	}
	if _, err := s.ListByTag(t.Context(), "pln_a", "batch", 1); err == nil {
		t.Error("closed DB returned runs")
	}
}
