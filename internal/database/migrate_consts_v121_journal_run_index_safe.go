package database

// migrationJournalRunIndexSafe (v121) hardens the v120 run_id generated
// column against non-JSON payloads.
//
// v120 defined `run_id AS json_extract(payload,'$.run_id')`. SQLite evaluates
// that expression at WRITE time for the partial index, so inserting ANY row
// whose payload isn't valid JSON fails with "malformed JSON" — a single bad
// writer would break journal writes wholesale, and v120 itself would fail to
// apply on an instance holding even one legacy non-JSON payload row.
//
// Wrap the extract in `json_valid(payload)` so a non-JSON payload yields NULL
// (excluded from the partial index) instead of an error. Rebuild the column
// + index. Drop the index first (a column referenced by an index can't be
// dropped). payload is NOT NULL DEFAULT '{}', so existing rows are unaffected.
const migrationJournalRunIndexSafe = `
DROP INDEX IF EXISTS idx_journal_ws_run;
ALTER TABLE journal_entries DROP COLUMN run_id;
ALTER TABLE journal_entries
  ADD COLUMN run_id TEXT
  GENERATED ALWAYS AS (
    CASE WHEN json_valid(payload) THEN json_extract(payload, '$.run_id') END
  ) VIRTUAL;
CREATE INDEX IF NOT EXISTS idx_journal_ws_run
  ON journal_entries(workspace_id, run_id)
  WHERE run_id IS NOT NULL;
`
