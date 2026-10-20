-- Explicit targeted human-message authority; exclude from backup/restore.
CREATE TABLE document_iteration_intents (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 submission_id TEXT NOT NULL CHECK(length(submission_id) BETWEEN 1 AND 128),
 page_id TEXT NOT NULL,
 schema_version INTEGER NOT NULL CHECK(schema_version=3),
 document_generation INTEGER NOT NULL CHECK(document_generation>=0),
 original_rev INTEGER NOT NULL CHECK(original_rev>=0),
 draft_rev INTEGER NOT NULL CHECK(draft_rev>=original_rev),
 pages_version INTEGER NOT NULL,
 policy_version INTEGER NOT NULL,
 member_id TEXT NOT NULL,
 member_revision INTEGER NOT NULL,
 chat_generation TEXT NOT NULL,
 chat_revision INTEGER NOT NULL,
 run_id TEXT UNIQUE REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 expires_at TEXT,
 revoked_at TEXT,
 UNIQUE(workspace_id,human_user_id,chat_id,submission_id),
 CHECK((run_id IS NULL)=(expires_at IS NULL))
);
CREATE INDEX document_iteration_page ON document_iteration_intents(workspace_id,page_id);
CREATE TRIGGER document_iteration_identity_immutable BEFORE UPDATE OF message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision ON document_iteration_intents
BEGIN SELECT RAISE(ABORT,'document iteration identity is immutable'); END;
CREATE TRIGGER document_iteration_binding_immutable BEFORE UPDATE OF run_id,expires_at ON document_iteration_intents
WHEN OLD.run_id IS NOT NULL AND (NEW.run_id IS NOT OLD.run_id OR NEW.expires_at IS NOT OLD.expires_at)
BEGIN SELECT RAISE(ABORT,'document iteration binding is immutable'); END;
CREATE TRIGGER document_iteration_cursor_monotonic BEFORE UPDATE OF draft_rev ON document_iteration_intents
WHEN NEW.draft_rev<OLD.draft_rev OR (OLD.run_id IS NULL AND NEW.draft_rev!=OLD.draft_rev)
BEGIN SELECT RAISE(ABORT,'document iteration cursor cannot rewind'); END;
