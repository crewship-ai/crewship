package database

// migrationAddPipelineSchedules (v80) introduces pipeline_schedules:
// cron triggers for saved pipelines. Each row binds a pipeline to a
// cron expression + workspace + optional inputs JSON; the scheduler
// fires Pipeline.Run(...) on the cron tick.
//
// Why a dedicated table (vs. extending the agents.schedule_* columns
// added in v24): pipelines are workspace-scoped assets distinct from
// agent records — a pipeline can be triggered without any agent
// owning the schedule, and the same pipeline can have multiple
// schedules (e.g. weekly digest + monthly report). Modeling this on
// the agents row would conflate agent-bound recurring runs with
// pipeline-bound recurring runs.
//
// Schema notes:
//   - id is "psched_" + CUID (so log lines pattern-match by entity).
//   - target_pipeline_id (not slug) — schedule pins to a stable id;
//     if the pipeline is renamed via slug change, the schedule
//     keeps working.
//   - target_pipeline_version optional pin (NULL = latest head).
//     Pinning is critical for production schedules: an agent
//     editing the pipeline shouldn't accidentally change what the
//     daily 8 AM run does.
//   - cron_expr stored as the literal string the user supplied;
//     parser is in the scheduler, not the DB.
//   - inputs_json carries the static inputs the schedule passes on
//     each fire (e.g. {"since":"yesterday"}).
//   - last_run_at + last_status give the UI a quick health glance
//     without joining journal_entries.
//   - next_run_at is denormalised cache of the next cron tick;
//     scheduler updates it after each fire so the dashboard can
//     show "next: tomorrow 8 AM" without re-parsing the cron expr
//     in the frontend.
const migrationAddPipelineSchedules = `
CREATE TABLE IF NOT EXISTS pipeline_schedules (
    id                       TEXT PRIMARY KEY,                            -- "psched_" + CUID
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,                               -- human-readable
    target_pipeline_id       TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    target_pipeline_version  INTEGER,                                     -- NULL = latest head_version
    cron_expr                TEXT NOT NULL,                               -- e.g. "0 8 * * *"
    timezone                 TEXT NOT NULL DEFAULT 'UTC',                 -- IANA name
    inputs_json              TEXT NOT NULL DEFAULT '{}',                  -- static inputs passed on every fire
    enabled                  INTEGER NOT NULL DEFAULT 1,
    last_run_at              TEXT,
    last_status              TEXT,                                        -- COMPLETED | FAILED | NULL
    last_run_id              TEXT,                                        -- pipeline_run id from journal
    next_run_at              TEXT,                                        -- denormalised next cron tick
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT
);
CREATE INDEX IF NOT EXISTS idx_pipeline_schedules_enabled
    ON pipeline_schedules (enabled, next_run_at)
    WHERE enabled = 1 AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pipeline_schedules_pipeline
    ON pipeline_schedules (target_pipeline_id)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pipeline_schedules_workspace
    ON pipeline_schedules (workspace_id, enabled)
    WHERE deleted_at IS NULL;
`
