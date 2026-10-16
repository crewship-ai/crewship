-- Optional verified execution correlation for source-backed chat decisions.
-- Legacy/service escalations deliberately remain unanchored; never infer a run
-- from their chat or a nearby message. Existing Inbox resolution is unchanged.
ALTER TABLE escalations ADD COLUMN source_run_id TEXT;
CREATE INDEX idx_escalations_execution_source ON escalations(workspace_id, chat_id, source_run_id) WHERE source_run_id IS NOT NULL;
