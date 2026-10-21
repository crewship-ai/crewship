-- Keep authority-parent foreign-key checks bounded during workspace cleanup.
-- Autonomous rows have no origin and need no index entry.
CREATE INDEX idx_assignments_chat_origin ON assignments(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_missions_chat_origin ON missions(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_pipelines_chat_origin ON pipelines(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_pipeline_versions_chat_origin ON pipeline_versions(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_pipeline_schedules_chat_origin ON pipeline_schedules(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_pipeline_runs_chat_origin ON pipeline_runs(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_pending_runs_chat_origin ON pending_runs(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_pipeline_drafts_chat_origin ON pipeline_drafts(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE INDEX idx_mission_comments_chat_origin ON mission_comments(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
