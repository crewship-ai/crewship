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
