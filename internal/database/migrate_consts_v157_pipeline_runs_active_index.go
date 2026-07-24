package database

// migrationPipelineRunsActiveIndexFix (v157) widens idx_pipeline_runs_active
// (v83) to match the queries it was built for.
//
// The v83 index's predicate is `status IN ('queued', 'running')`, but
// RunStore.ListActive and RunStore.ListInFlight (internal/pipeline/runs.go)
// both filter `status IN ('queued', 'running', 'waiting')` — 'waiting' was
// added for wait(approval) steps after v83 landed and the index predicate
// was never updated to match. A query predicate that isn't a subset of a
// partial index's predicate can't use that index, so SQLite has been
// full-scanning (well, scanning the broader (workspace_id, status) index
// from v83, missing the started_at ordering) for both call sites while
// still paying to maintain the now-pointless narrower index on every write
// that touches a queued/running row.
//
// See issue #1411 (2026-07-24 perf audit).
const migrationPipelineRunsActiveIndexFix = `
DROP INDEX IF EXISTS idx_pipeline_runs_active;
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_active
    ON pipeline_runs (workspace_id, started_at DESC)
    WHERE status IN ('queued', 'running', 'waiting');
`
