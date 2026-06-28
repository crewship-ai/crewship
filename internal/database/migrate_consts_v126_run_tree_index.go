package database

// migrationRunTreeIndex (v126) indexes pipeline_runs.triggered_by_id so
// the parent/child run-tree recursive CTE (RunStore.RunTree) joins
// `r.triggered_by_id = t.id` via an index instead of a full table scan
// per recursion level. Workspace-prefixed to match the scoped query.
// Additive, idempotent.
const migrationRunTreeIndex = `
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_triggered_by
    ON pipeline_runs (workspace_id, triggered_by_id)
    WHERE triggered_by_id IS NOT NULL;
`
