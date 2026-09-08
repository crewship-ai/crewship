-- Durable retries for explicit fresh-history continuation into another room.
CREATE TABLE workspace_conversation_continuations (
 source_conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 requested_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 client_id TEXT NOT NULL,
 request_json TEXT NOT NULL,
 target_conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 PRIMARY KEY(source_conversation_id,requested_by_user_id,client_id)
);
CREATE INDEX idx_conversation_continuations_user ON workspace_conversation_continuations(requested_by_user_id);
CREATE INDEX idx_conversation_continuations_target ON workspace_conversation_continuations(target_conversation_id);
