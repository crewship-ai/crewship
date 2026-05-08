package database

// migrationAddPipelineVersionsAndWaitpoints (v79) adds two
// pipeline-related tables that build on v78:
//
//  1. pipeline_versions — immutable history of every save. Lets
//     callers pin a run to a specific version (reproducibility),
//     roll back the head pointer to a known-good version, and
//     compute diffs between revisions for the marketplace
//     integrity story. v78's pipelines.head_version is the
//     pointer; pipeline_versions is the actual history.
//
//  2. pipeline_waitpoints — token-keyed pause points for
//     StepWait approval steps. The executor parks on a channel
//     waiting for an HTTP completion; the persisted row makes
//     the wait survive process restarts (recovery scan re-attaches
//     pending waitpoints to running pipeline goroutines).
//
// Both tables are workspace-scoped via FK to pipelines (versions)
// or directly via workspace_id (waitpoints — keeps the inbox
// query simple without a join).
//
// pipeline_versions:
//   - version is monotonic per pipeline_id (1, 2, 3, ...)
//   - definition_json stored verbatim so historical runs replay
//     against the exact bytes that were tested at that version
//   - definition_hash = sha256 of definition_json — content-hash
//     dedup at save time (no-op when re-saving identical content)
//   - parent_version + change_summary capture provenance for
//     diff UIs
//
// pipeline_waitpoints:
//   - token is short-lived random string the inbox UI / approve
//     endpoint POSTs back with the decision
//   - status = pending | approved | denied | timed_out | cancelled
//   - timeout_at is computed at create time from step.TimeoutSec
//   - decision_payload optionally carries data the approver
//     attached (comment, modified payload) — flows back to the
//     waiting goroutine via WaitpointStore.WaitFor
const migrationAddPipelineVersionsAndWaitpoints = `
CREATE TABLE IF NOT EXISTS pipeline_versions (
    id              TEXT PRIMARY KEY,                                 -- "plnv_" + CUID
    pipeline_id     TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,                                 -- monotonic per pipeline_id
    definition_json TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    author_type     TEXT NOT NULL CHECK (author_type IN ('agent','user','system','imported')),
    author_id       TEXT NOT NULL,
    parent_version  INTEGER,
    change_summary  TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    UNIQUE (pipeline_id, version),
    UNIQUE (pipeline_id, definition_hash)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_versions_pipeline ON pipeline_versions (pipeline_id, version DESC);

ALTER TABLE pipelines ADD COLUMN head_version INTEGER NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS pipeline_waitpoints (
    token              TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pipeline_run_id    TEXT NOT NULL,                                  -- run id from journal
    step_id            TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('approval','event')),
    prompt             TEXT,
    invoking_crew_id   TEXT,
    status             TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','approved','denied','timed_out','cancelled')),
    decision_payload   TEXT,                                           -- JSON; optional approver-supplied data
    decided_by_user_id TEXT,
    timeout_at         TEXT NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    decided_at         TEXT
);
CREATE INDEX IF NOT EXISTS idx_pipeline_waitpoints_workspace_pending
    ON pipeline_waitpoints (workspace_id, status, timeout_at)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_pipeline_waitpoints_run
    ON pipeline_waitpoints (pipeline_run_id);
`
