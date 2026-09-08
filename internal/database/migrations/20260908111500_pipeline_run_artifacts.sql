-- Output ownership reuses the existing workspace content-addressed blob store.
CREATE TABLE pipeline_run_artifacts (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
  step_execution_id TEXT NOT NULL REFERENCES pipeline_step_executions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  label TEXT NOT NULL,
  state TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(step_execution_id,kind,label,source)
);
CREATE INDEX idx_pipeline_run_artifacts_run ON pipeline_run_artifacts(run_id,created_at,id);
CREATE INDEX idx_pipeline_run_artifacts_blob ON pipeline_run_artifacts(sha256) WHERE sha256<>'';
