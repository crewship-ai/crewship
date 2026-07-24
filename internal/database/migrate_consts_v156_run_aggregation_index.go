package database

// migrationRunAggregationIndex (v156) narrows the index the run_aggregates
// CTE (journal.ListRuns / countRuns / RunStats / RunInsights) relies on.
//
// idx_journal_ws_trace (v60) is (workspace_id, trace_id) WHERE trace_id IS
// NOT NULL — it matches every traced row (llm.call, exec.command,
// run.agent_span, ...), not just the ~2 run.* rows per run. On a workspace
// with heavy per-step tracing, the run aggregation queries scan every traced
// row in the workspace to reconstruct a handful of run.* rows per trace_id.
//
// This migration adds a second, narrower partial index whose predicate
// matches the `entry_type LIKE 'run.%'` condition already present in
// runAggregatesCTE's innerWhere (see internal/journal/runs.go) verbatim, so
// SQLite's partial-index matching picks it over the broader v60 index.
// ts is included so ORDER BY started_at DESC-adjacent scans (the CTE's
// GROUP BY trace_id, then outer ORDER BY started_at) can use it too.
//
// See issue #1411 (2026-07-24 perf audit).
const migrationRunAggregationIndex = `
CREATE INDEX IF NOT EXISTS idx_journal_ws_trace_run
    ON journal_entries(workspace_id, trace_id, entry_type, ts)
    WHERE entry_type LIKE 'run.%';
`
