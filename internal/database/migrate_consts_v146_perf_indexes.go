package database

// migrationPerfIndexes (v146) adds three composite indexes that back hot
// filtered/range queries which currently fall back to broader scans. All
// additive and idempotent (CREATE INDEX IF NOT EXISTS), so re-runs and
// restore replays are safe.
//
//   - idx_journal_ws_type_ts: the live journal_entries table only carries
//     (workspace_id, ts) and (entry_type, ts) separately, so a
//     single-entry_type filtered List/Count within a workspace scans one
//     index and filters the other column. The archived table already has
//     the equivalent (idx_archived_ws_type); this brings the live table to
//     parity.
//   - idx_journal_ws_sev_ts: workspace-scoped twin of the existing partial
//     idx_journal_severity (same WHERE severity IN ('warn','error')
//     predicate) for the per-workspace errors/warnings view. Without the
//     workspace_id prefix that global partial index can't prune to one
//     tenant.
//   - idx_peer_conv_crew_created: the standup query filters crew_id +
//     created_at range and ORDER BYs created_at, but only idx_peer_conv_crew
//     (crew_id) exists — the composite lets SQLite range-scan created_at
//     within a crew instead of sorting after the fact.
const migrationPerfIndexes = `
CREATE INDEX IF NOT EXISTS idx_journal_ws_type_ts ON journal_entries(workspace_id, entry_type, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_ws_sev_ts ON journal_entries(workspace_id, severity, ts DESC) WHERE severity IN ('warn','error');
CREATE INDEX IF NOT EXISTS idx_peer_conv_crew_created ON peer_conversations(crew_id, created_at);
`
