-- Explicit human message consent. Ordinary conversation metadata is never
-- sufficient to insert this installation-bound authority record.
CREATE TABLE document_creation_consents (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 submission_id TEXT NOT NULL CHECK(length(submission_id) BETWEEN 1 AND 128),
 run_id TEXT UNIQUE,
 expires_at TEXT,
 revoked_at TEXT,
 consumed_at TEXT,
 result_page_id TEXT REFERENCES pages(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 UNIQUE(workspace_id,human_user_id,chat_id,submission_id),
 CHECK((run_id IS NULL) = (expires_at IS NULL))
);
CREATE INDEX document_creation_consents_human ON document_creation_consents(human_user_id);
CREATE INDEX document_creation_consents_chat ON document_creation_consents(chat_id);
CREATE INDEX document_creation_consents_page ON document_creation_consents(result_page_id);
CREATE TRIGGER document_creation_consent_identity_immutable BEFORE UPDATE OF message_id,workspace_id,human_user_id,chat_id,submission_id ON document_creation_consents
BEGIN SELECT RAISE(ABORT,'document creation consent identity is immutable'); END;
CREATE TRIGGER document_creation_consent_run_immutable BEFORE UPDATE OF run_id,expires_at ON document_creation_consents
WHEN OLD.run_id IS NOT NULL AND (NEW.run_id IS NOT OLD.run_id OR NEW.expires_at IS NOT OLD.expires_at)
BEGIN SELECT RAISE(ABORT,'document creation run binding is immutable'); END;

-- Durable creation provenance is separate from the nonportable consent above.
-- Run IDs are opaque audit identities, never restored live authority.
CREATE TABLE document_creations (
 page_id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL UNIQUE,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 source_chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
 submission_id TEXT NOT NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
 revision INTEGER NOT NULL DEFAULT 0 CHECK(revision=0),
 created_at TEXT NOT NULL,
 FOREIGN KEY(workspace_id,page_id) REFERENCES documents(workspace_id,page_id) ON DELETE CASCADE
);
CREATE INDEX document_creations_workspace ON document_creations(workspace_id,page_id);
CREATE INDEX document_creations_agent ON document_creations(agent_id);
CREATE INDEX document_creations_human ON document_creations(human_user_id);
CREATE INDEX document_creations_chat ON document_creations(source_chat_id);
