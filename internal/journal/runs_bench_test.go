package journal

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"

	_ "modernc.org/sqlite"
)

// benchSchemaSQL is schemaSQL (journal_test.go) minus its indexes — the
// benchmarks below add exactly the index(es) under test so each variant's
// query plan is deterministic instead of depending on schemaSQL's current
// index set.
const benchSchemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_bench');

CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    priority TEXT NOT NULL DEFAULT 'normal',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT,
    seq INTEGER NOT NULL DEFAULT 0,
    prev_hash TEXT NOT NULL DEFAULT '',
    entry_hash TEXT NOT NULL DEFAULT ''
);
`

// seedRunAggregationBenchData populates nRuns runs (run.started +
// run.completed, 2 rows each) plus noisePerRun non-run traced rows sharing
// each run's trace_id (llm.call / exec.command — the entry types the #1411
// audit found idx_journal_ws_trace matches indiscriminately alongside run.*
// rows). Uses one prepared statement in a single transaction so setup time
// doesn't dominate the benchmark at realistic row counts.
func seedRunAggregationBenchData(b *testing.B, db *sql.DB, nRuns, noisePerRun int) {
	b.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO journal_entries (id, workspace_id, ts, entry_type, actor_type, summary, trace_id)
VALUES (?, ?, ?, ?, 'sidecar', 'x', ?)`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	defer stmt.Close()

	base := time.Now().UTC().Add(-24 * time.Hour)
	rowID := 0
	nextID := func() string { rowID++; return fmt.Sprintf("row_%d", rowID) }

	for r := 0; r < nRuns; r++ {
		traceID := fmt.Sprintf("run_%d", r)
		startTS := base.Add(time.Duration(r) * time.Second)
		if _, err := stmt.ExecContext(ctx, nextID(), "ws_bench", tsformat.Format(startTS.UTC()), "run.started", traceID); err != nil {
			b.Fatalf("seed run.started: %v", err)
		}
		if _, err := stmt.ExecContext(ctx, nextID(), "ws_bench", tsformat.Format(startTS.Add(time.Minute).UTC()), "run.completed", traceID); err != nil {
			b.Fatalf("seed run.completed: %v", err)
		}
		for n := 0; n < noisePerRun; n++ {
			ts := startTS.Add(time.Duration(n) * time.Millisecond)
			entryType := "llm.call"
			if n%2 == 0 {
				entryType = "exec.command"
			}
			if _, err := stmt.ExecContext(ctx, nextID(), "ws_bench", tsformat.Format(ts.UTC()), entryType, traceID); err != nil {
				b.Fatalf("seed noise row: %v", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
}

// BenchmarkListRuns_BroadIndexOnly reproduces the PRE-#1411 state: only the
// v60 idx_journal_ws_trace index exists, which matches every traced row
// (workspace_id, trace_id) regardless of entry_type — the run_aggregates CTE
// must scan every llm.call/exec.command row per run to reconstruct the
// handful of run.* rows it actually needs.
func BenchmarkListRuns_BroadIndexOnly(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), benchSchemaSQL); err != nil {
		b.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE INDEX idx_journal_ws_trace ON journal_entries(workspace_id, trace_id) WHERE trace_id IS NOT NULL`); err != nil {
		b.Fatalf("broad index: %v", err)
	}
	seedRunAggregationBenchData(b, db, 2000, 100)
	if _, err := db.ExecContext(context.Background(), "ANALYZE"); err != nil {
		b.Fatalf("analyze: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_bench", Limit: 50}); err != nil {
			b.Fatalf("list runs: %v", err)
		}
	}
}

// BenchmarkListRuns_WithNarrowRunIndex is the #1411 fix: migration v153 adds
// a (workspace_id, trace_id, entry_type, ts) partial index scoped to
// entry_type LIKE 'run.%', which the run_aggregates CTE's inner WHERE
// matches exactly. Compare ns/op against BenchmarkListRuns_BroadIndexOnly at
// the same data volume.
//
// Requires ANALYZE (see below) to actually land on the narrow index: without
// fresh sqlite_stat1 rows, SQLite's planner has no row-count evidence that
// idx_journal_ws_trace_run is far more selective than the older
// idx_journal_ws_trace on the same leading column, and keeps picking the
// broad one even though the narrow one perfectly matches the WHERE clause —
// this benchmark is what surfaced that gap, which migrate.go's Migrate now
// closes with a post-migration ANALYZE.
func BenchmarkListRuns_WithNarrowRunIndex(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), benchSchemaSQL); err != nil {
		b.Fatalf("schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE INDEX idx_journal_ws_trace ON journal_entries(workspace_id, trace_id) WHERE trace_id IS NOT NULL`); err != nil {
		b.Fatalf("broad index: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE INDEX idx_journal_ws_trace_run ON journal_entries(workspace_id, trace_id, entry_type, ts) WHERE entry_type LIKE 'run.%'`); err != nil {
		b.Fatalf("narrow run index: %v", err)
	}
	seedRunAggregationBenchData(b, db, 2000, 100)
	if _, err := db.ExecContext(context.Background(), "ANALYZE"); err != nil {
		b.Fatalf("analyze: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_bench", Limit: 50}); err != nil {
			b.Fatalf("list runs: %v", err)
		}
	}
}
