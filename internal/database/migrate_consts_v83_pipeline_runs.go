package database

// migrationAddPipelineRuns (v83) introduces a dedicated pipeline_runs
// table to persist per-run state across process restarts.
//
// Pre-v83 design (carried from MVP): runs lived only in journal_entries
// (synthetic types pipeline.run.* and pipeline.step.*) plus an
// in-memory RunRegistry. Restart of the binary lost active runs
// entirely — `run.started` events with no matching terminal — and
// list-runs queries had to LIKE-scan journal_entries with json_extract
// on every page load.
//
// v83 promotes runs to a first-class entity with column-typed
// fields:
//   - status enum makes "list active runs" a B-tree scan instead of
//     a journal LIKE + payload parse. Indexed by (workspace_id, status)
//     so the active-runs panel is O(active runs in workspace).
//   - current_step_id + step_outputs_json gives the boot recovery
//     scan enough to mark in-flight runs interrupted (we can't
//     actually resume — the executor goroutine is gone — but we can
//     close the audit story instead of leaving open run.started
//     events forever).
//   - cost_usd + duration_ms denormalized from journal so dashboards
//     don't reaggregate on every render.
//   - error_fingerprint precomputed at terminal — empty in this
//     migration, populated by the follow-up errors-fingerprinting
//     PR. Indexed now so adding the populator doesn't require a
//     reindex on a large table.
//
// Journal entries continue to land alongside this table — they're
// the audit log + WS event firehose. pipeline_runs is the
// query-optimized projection. Two writes per run-state change is
// acceptable for the read-side speedup.
//
// Schema notes:
//   - id uses "prn_" prefix (pipeline run); existing run_id values
//     ("run_<cuid>") in the journal continue to work — we just store
//     them as-is rather than renaming. Greppability, not change.
//   - status enum is an unconstrained TEXT column rather than CHECK
//     constraint so future statuses (e.g., "skipped" for a workspace-
//     level pause feature) don't require a migration. Validation
//     happens in the Go layer where the Status type is closed.
//   - idempotency_key is the dedupe identifier supplied by the caller
//     (webhook delivery, retry) — separate from the runs.id so
//     multiple actual runs can share a key (only one wins the gate;
//     the others get the winner's run_id pointer back).
//   - mode (run | test_run | dry_run) so the same table backs all
//     three; queries can filter dry_run out of the user-facing UI.
const migrationAddPipelineRuns = `
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id                  TEXT PRIMARY KEY,                              -- "prn_" or "run_" + CUID
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pipeline_id         TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    pipeline_slug       TEXT NOT NULL,                                 -- denormalized for list view
    pipeline_version    INTEGER,                                       -- NULL = HEAD; matches pipelines.head_version
    status              TEXT NOT NULL,                                 -- queued | running | completed | failed | cancelled | dry_run | interrupted
    mode                TEXT NOT NULL DEFAULT 'run',                   -- run | test_run | dry_run
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    current_step_id     TEXT,                                          -- last step we entered; populated for in-flight + interrupted
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',                    -- map step_id -> output (final or partial-on-interrupt)
    output              TEXT,                                          -- final pipeline output (last step's output for linear, leaf node's for DAG)
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT,                                          -- non-empty when status IN (failed, interrupted)
    failed_at_step      TEXT,                                          -- step_id of the failure point
    error_fingerprint   TEXT,                                          -- populated by follow-up errors PR; pre-indexed
    invoking_crew_id    TEXT,                                          -- cross-crew reuse audit
    invoking_agent_id   TEXT,                                          -- agent that triggered the run (when applicable)
    invoking_user_id    TEXT,                                          -- user that triggered manually (when applicable)
    triggered_via       TEXT NOT NULL DEFAULT 'manual',                -- manual | schedule | webhook | call_pipeline
    triggered_by_id     TEXT,                                          -- schedule_id / webhook_id / parent run_id
    idempotency_key     TEXT,                                          -- caller-supplied; multiple runs can share if dedupe collisions
    inputs_json         TEXT NOT NULL DEFAULT '{}',                    -- captured invocation inputs for replay
    concurrency_key     TEXT,                                          -- per-run concurrency gate; matches pipeline.concurrency_key
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_workspace_status
    ON pipeline_runs (workspace_id, status);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_pipeline_started
    ON pipeline_runs (pipeline_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_active
    ON pipeline_runs (workspace_id, started_at DESC)
    WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_fingerprint
    ON pipeline_runs (workspace_id, error_fingerprint, started_at DESC)
    WHERE error_fingerprint IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_idempotency
    ON pipeline_runs (workspace_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
`
