-- Capture the effective recipe, including per-step overrides, at run start.
ALTER TABLE pipeline_runs ADD COLUMN executed_definition_json TEXT;
-- Append-only attempts. Output is snapshotted text/JSON, never a mutable file path.
CREATE TABLE pipeline_step_executions (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    parent_execution_id TEXT REFERENCES pipeline_step_executions(id),
    step_id TEXT NOT NULL,
    execution_path TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    agent_slug TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    ended_at TEXT,
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    UNIQUE(run_id, execution_path, attempt)
);
CREATE INDEX idx_pipeline_step_executions_run ON pipeline_step_executions(run_id, started_at, id);
CREATE INDEX idx_pipeline_step_executions_parent ON pipeline_step_executions(parent_execution_id);
