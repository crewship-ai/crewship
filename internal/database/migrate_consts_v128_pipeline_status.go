package database

// migrationPipelineStatus (v128) adds a lifecycle status column to the
// pipelines table for the routine-governance maker-checker flow.
//
// Values:
//   - active   — live and runnable (the default; all pre-v128 rows backfill here)
//   - proposed — agent/user-authored but flagged risky; awaiting MANAGER+
//     approval before it can run (see the risk classifier in
//     internal/api/pipeline_governance.go)
//   - disabled — admin "airbag"; an OWNER/ADMIN killed the routine. In-flight
//     runs are cancelled at disable time and new runs are refused.
//
// This is a pure ADD COLUMN with a constant NOT NULL DEFAULT, so SQLite
// backfills every existing row to 'active' automatically — no table rebuild,
// no new table (hence no BackupTableIntent registration is required; the
// backup subsystem already enumerates pipelines and picks up the new column).
// The redundant UPDATE is belt-and-braces for any driver that somehow leaves
// the column NULL. The partial index backs the `status` list filter +
// the proposed-queue review surface. Additive, idempotent.
const migrationPipelineStatus = `
ALTER TABLE pipelines
    ADD COLUMN status TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','proposed','disabled'));

UPDATE pipelines SET status = 'active' WHERE status IS NULL OR status = '';

CREATE INDEX IF NOT EXISTS idx_pipelines_workspace_status
    ON pipelines (workspace_id, status)
    WHERE deleted_at IS NULL;
`
