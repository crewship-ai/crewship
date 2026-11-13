-- Agent access modes (D1, #5317). A chat message may carry structured
-- references to artifacts (composer chips, never text the model reads). The
-- trusted pre-send recorder resolves each against the sending human, as an
-- ordinary member, and keeps what it admitted here. Generic transcript
-- metadata, an internal token or an agent cannot insert this table: only
-- RecordArtifactRefs writes it.
CREATE TABLE IF NOT EXISTS artifact_message_refs (
 message_id TEXT NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 PRIMARY KEY(message_id, page_id)
);
CREATE INDEX IF NOT EXISTS artifact_message_refs_page ON artifact_message_refs(page_id);
CREATE INDEX IF NOT EXISTS artifact_message_refs_human ON artifact_message_refs(human_user_id);
CREATE INDEX IF NOT EXISTS artifact_message_refs_chat ON artifact_message_refs(chat_id);
CREATE INDEX IF NOT EXISTS artifact_message_refs_workspace ON artifact_message_refs(workspace_id);
CREATE TRIGGER IF NOT EXISTS artifact_message_refs_immutable BEFORE UPDATE ON artifact_message_refs
BEGIN SELECT RAISE(ABORT,'artifact message reference is immutable'); END;

-- The run's reach: exactly the references of the human message that started
-- it. Written only by run admission, in the transaction that captures the run
-- principal. The trigger is the same rule where code cannot forget it: a row
-- must mirror a recorded reference of that run's own principal message and
-- human. A follow-up message carries its own references or none.
CREATE TABLE IF NOT EXISTS artifact_run_scope (
 run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 message_id TEXT NOT NULL,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 PRIMARY KEY(run_id, page_id),
 FOREIGN KEY(message_id, page_id) REFERENCES artifact_message_refs(message_id, page_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS artifact_run_scope_page ON artifact_run_scope(page_id);
CREATE INDEX IF NOT EXISTS artifact_run_scope_workspace ON artifact_run_scope(workspace_id);
CREATE INDEX IF NOT EXISTS artifact_run_scope_ref ON artifact_run_scope(message_id, page_id);
CREATE TRIGGER IF NOT EXISTS artifact_run_scope_mirrors_principal BEFORE INSERT ON artifact_run_scope
WHEN NOT EXISTS(
 SELECT 1 FROM artifact_run_principals p
 JOIN artifact_message_refs r ON r.message_id=p.message_id AND r.workspace_id=p.workspace_id
  AND r.human_user_id=p.human_user_id AND r.chat_id=p.chat_id
 WHERE p.run_id=NEW.run_id AND p.workspace_id=NEW.workspace_id AND p.message_id=NEW.message_id AND r.page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'artifact run scope must mirror its human message references'); END;
CREATE TRIGGER IF NOT EXISTS artifact_run_scope_immutable BEFORE UPDATE ON artifact_run_scope
BEGIN SELECT RAISE(ABORT,'artifact run scope is immutable'); END;

-- Off for every workspace until an admin turns it on (instance default or a
-- per-workspace override: "crewship feature-flag ..."). Off changes nothing.
INSERT OR IGNORE INTO feature_flags (id, key, description, enabled, percentage)
VALUES ('ff_artifact_agent_access_modes', 'artifact_agent_access_modes',
 'Agent access modes for artifacts (#5317): chat messages carry structured artifact references that bound the agent run.', 0, 100);
