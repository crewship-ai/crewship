-- A routine step's conversation keeps an explicit source, independent of its title.
-- Existing chats remain unlinked: names and timestamps are not reliable evidence.
ALTER TABLE chats ADD COLUMN pipeline_run_id TEXT REFERENCES pipeline_runs(id) ON DELETE SET NULL;
ALTER TABLE chats ADD COLUMN pipeline_step_id TEXT;
CREATE INDEX idx_chats_pipeline_run ON chats(pipeline_run_id, workspace_id) WHERE pipeline_run_id IS NOT NULL;
