package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV157_PipelineRunsActiveIndexFix verifies the #1411 perf fix:
// idx_pipeline_runs_active now includes 'waiting' in its predicate, so the
// query planner picks it for RunStore.ListActive and RunStore.ListInFlight
// (internal/pipeline/runs.go), which both filter on all three statuses. The
// pre-v157 index (queued/running only) could never be chosen for these
// queries since its predicate wasn't a superset of the query's.
func TestMigrateV157_PipelineRunsActiveIndexFix(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v157.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name=?`,
		"idx_pipeline_runs_active").Scan(&name); err != nil {
		t.Fatalf("index idx_pipeline_runs_active missing: %v", err)
	}

	// ListActive's shape: workspace-scoped, all three statuses, ordered by
	// started_at DESC.
	var plan string
	if err := db.QueryRow(
		`EXPLAIN QUERY PLAN SELECT id FROM pipeline_runs
		 WHERE workspace_id = 'ws_x' AND status IN ('queued','running','waiting')
		 ORDER BY started_at DESC`).
		Scan(new(int), new(int), new(int), &plan); err != nil {
		t.Fatalf("explain ListActive shape: %v", err)
	}
	if !strings.Contains(plan, "idx_pipeline_runs_active") {
		t.Errorf("ListActive query plan did not use idx_pipeline_runs_active: %q", plan)
	}
}
