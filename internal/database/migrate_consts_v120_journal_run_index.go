package database

// migrationJournalRunIndex (v120) makes "logs for one run" an indexed lookup
// instead of a full workspace scan of journal_entries.
//
// Pipeline runs tag their journal entries with the run id in payload.run_id
// (NOT trace_id), so the dock's run-logs query filters on
// `json_extract(payload,'$.run_id')`. With no index on that expression, the
// OR-with-trace_id predicate cannot be index-unioned and SQLite scans the
// whole workspace partition per Logs-tab open — the exact anti-pattern
// migration v83 created pipeline_runs to escape.
//
// A VIRTUAL generated column surfaces payload.run_id as an indexable column;
// the partial index (run_id IS NOT NULL) keeps it tiny since most entries
// carry no run_id. The run-logs query then unions two B-tree probes
// (trace_id index + this one). Additive and backward-compatible — VIRTUAL
// columns store nothing and never touch existing rows.
const migrationJournalRunIndex = `
ALTER TABLE journal_entries
  ADD COLUMN run_id TEXT
  GENERATED ALWAYS AS (json_extract(payload, '$.run_id')) VIRTUAL;
CREATE INDEX IF NOT EXISTS idx_journal_ws_run
  ON journal_entries(workspace_id, run_id)
  WHERE run_id IS NOT NULL;
`
