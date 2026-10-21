-- Fresh invocation correlation is independent of inherited recipe authority.
-- Legacy and scheduled runs remain NULL and keep their existing execution path.
ALTER TABLE pipeline_runs ADD COLUMN invocation_source_run_id TEXT;
CREATE INDEX idx_pipeline_runs_invocation_source ON pipeline_runs(workspace_id, invocation_source_run_id) WHERE invocation_source_run_id IS NOT NULL;
