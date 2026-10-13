-- Historical linkage, never runtime authority. Only publication with a verified
-- agent run principal supplies this field; human revisions leave it NULL.
ALTER TABLE page_artifact_revisions ADD COLUMN source_chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL;
CREATE INDEX idx_artifact_revision_source_chat ON page_artifact_revisions(source_chat_id,created_at DESC);
CREATE INDEX idx_artifact_request_chat_result ON page_artifact_requests(chat_id,status,page_id,result_revision,agent_id,run_id);
CREATE TRIGGER artifact_revision_source_chat_scope BEFORE INSERT ON page_artifact_revisions
WHEN NEW.source_chat_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM chats c JOIN pages p ON p.workspace_id=c.workspace_id
 WHERE p.id=NEW.page_id AND c.id=NEW.source_chat_id AND c.agent_id=NEW.author_agent_id)
BEGIN SELECT RAISE(ABORT,'artifact source chat must match workspace and author agent'); END;
-- FK deletion may clear historical linkage; it cannot be reassigned to another
-- chat after publication. Restore remaps the ID before its initial INSERT.
CREATE TRIGGER artifact_revision_source_chat_immutable BEFORE UPDATE OF source_chat_id ON page_artifact_revisions
WHEN OLD.source_chat_id IS NOT NEW.source_chat_id AND (NEW.source_chat_id IS NOT NULL OR EXISTS(SELECT 1 FROM chats WHERE id=OLD.source_chat_id))
BEGIN SELECT RAISE(ABORT,'artifact source chat is immutable'); END;
