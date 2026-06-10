package database

// migrationMetricsStatusIndexes (v113) backs the /metrics domain gauges
// (W10, RELEASE-1.0-HARDENING): the handler counts assignments and
// pipeline runs grouped by status on every cache refresh, and neither
// table had a plain status index —
//
//   - assignments only has the partial idx_assignments_status_queued_at
//     (WHERE status = 'QUEUED'), which can't serve a full GROUP BY
//     status;
//   - pipeline_runs indexes status only behind workspace_id
//     (idx_pipeline_runs_workspace_status), so a cross-workspace
//     GROUP BY status is a table scan.
//
// Both new indexes are tiny (single low-cardinality TEXT column) and
// additive — no table rebuild, instant on existing deployments.
const migrationMetricsStatusIndexes = `
CREATE INDEX IF NOT EXISTS idx_assignments_status ON assignments(status);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_status ON pipeline_runs(status);
`
