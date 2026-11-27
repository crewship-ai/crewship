-- A Sheet a person opened from a file in a chat belongs to that chat from the moment it is created
-- (initialize with X-Craft-Chat), before any message names it, so the chat's Artifacts list shows
-- it at once. Additive, no backfill: artifact_message_refs is unchanged and stays the record of
-- what a message attached (and of any consent).
CREATE TABLE IF NOT EXISTS artifact_chat_refs (
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 PRIMARY KEY(chat_id, page_id)
);
CREATE INDEX IF NOT EXISTS artifact_chat_refs_page ON artifact_chat_refs(page_id);
CREATE INDEX IF NOT EXISTS artifact_chat_refs_human ON artifact_chat_refs(human_user_id);
CREATE INDEX IF NOT EXISTS artifact_chat_refs_workspace ON artifact_chat_refs(workspace_id);
-- The same rule as a message reference: the human's own chat, and a page of the same workspace.
CREATE TRIGGER IF NOT EXISTS artifact_chat_refs_own_chat BEFORE INSERT ON artifact_chat_refs
WHEN NOT EXISTS(
 SELECT 1 FROM chats c
 JOIN pages p ON p.id=NEW.page_id AND p.workspace_id=NEW.workspace_id
 WHERE c.id=NEW.chat_id AND c.workspace_id=NEW.workspace_id AND c.created_by=NEW.human_user_id)
BEGIN SELECT RAISE(ABORT,'artifact chat reference must name the human''s own chat and a page of the workspace'); END;
CREATE TRIGGER IF NOT EXISTS artifact_chat_refs_immutable BEFORE UPDATE ON artifact_chat_refs
BEGIN SELECT RAISE(ABORT,'artifact chat references are immutable'); END;
