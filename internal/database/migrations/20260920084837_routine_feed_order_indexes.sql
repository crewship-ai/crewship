-- Keep the workspace routine run feed and schedule list ordered by their
-- primary display key without scanning/sorting every tenant's history.
-- The schedule index is partial because that read path excludes tombstones.
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_workspace_started
    ON pipeline_runs(workspace_id, started_at DESC);

CREATE INDEX IF NOT EXISTS idx_pipeline_schedules_workspace_next_run
    ON pipeline_schedules(workspace_id, next_run_at)
    WHERE deleted_at IS NULL;
