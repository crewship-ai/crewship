-- Preserve the deployed migration; complete the schedule ordering index in a new version.
DROP INDEX IF EXISTS idx_pipeline_schedules_workspace_next_run;
CREATE INDEX idx_pipeline_schedules_workspace_next_run
    ON pipeline_schedules(workspace_id, next_run_at, id)
    WHERE deleted_at IS NULL;
