package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV156_RunAggregationIndex verifies the #1411 perf migration: a
// (workspace_id, trace_id, entry_type, ts) partial index scoped to
// entry_type LIKE 'run.%' exists after Migrate, and the run_aggregates CTE's
// grouping query (internal/journal/runs.go) picks it over the broader v60
// idx_journal_ws_trace index.
func TestMigrateV156_RunAggregationIndex(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v156.db"))
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
		"idx_journal_ws_trace_run").Scan(&name); err != nil {
		t.Fatalf("index idx_journal_ws_trace_run missing: %v", err)
	}

	// Mirrors runAggregatesCTE's innerWhere: workspace_id = ?, trace_id IS
	// NOT NULL, entry_type LIKE 'run.%'. The query planner should pick the
	// narrower v156 index over the broader v60 one for this shape.
	var plan string
	if err := db.QueryRow(
		`EXPLAIN QUERY PLAN SELECT trace_id FROM journal_entries
		 WHERE workspace_id = 'ws_x' AND trace_id IS NOT NULL AND entry_type LIKE 'run.%'
		 GROUP BY trace_id`).
		Scan(new(int), new(int), new(int), &plan); err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(plan, "idx_journal_ws_trace_run") {
		t.Errorf("query plan did not use idx_journal_ws_trace_run: %q", plan)
	}
}
