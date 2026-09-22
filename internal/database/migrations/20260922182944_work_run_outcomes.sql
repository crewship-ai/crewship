-- Capture output before dispatch settlement. The fenced attempt transition owns
-- run_status; projection may retry after a restart without executing the agent.
ALTER TABLE work_attempts ADD COLUMN run_result_json TEXT;
ALTER TABLE work_attempts ADD COLUMN run_status TEXT NOT NULL DEFAULT ''
    CHECK (run_status IN ('', 'COMPLETED', 'FAILED', 'CANCELLED'));
ALTER TABLE work_attempts ADD COLUMN run_projected_at TEXT;
ALTER TABLE work_attempts ADD COLUMN run_projection_attempted_at TEXT;
CREATE INDEX idx_work_attempts_run_projection ON work_attempts(COALESCE(run_projection_attempted_at, started_at), run_id)
    WHERE run_result_json IS NOT NULL AND run_status != '' AND run_projected_at IS NULL;
