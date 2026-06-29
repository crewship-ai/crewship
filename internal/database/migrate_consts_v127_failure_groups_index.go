package database

// migrationFailureGroupsIndex (v127) backs RunStore.FailureGroups, which
// groups a workspace's failed runs by error_fingerprint (newest-first) for
// the errors view + bulk replay. Before this index that query had to scan
// every failed row in the workspace; the partial, workspace-prefixed index
// — ordered by started_at DESC within each fingerprint — lets both the
// SQL-side GROUP BY and the per-fingerprint "most recent N run ids" window
// query resolve through the index instead of a full table scan. The partial
// predicate matches the query's WHERE exactly so only the relevant rows are
// indexed. Additive, idempotent.
const migrationFailureGroupsIndex = `
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_failure_groups
    ON pipeline_runs (workspace_id, error_fingerprint, started_at DESC)
    WHERE status = 'failed' AND error_fingerprint IS NOT NULL;
`
