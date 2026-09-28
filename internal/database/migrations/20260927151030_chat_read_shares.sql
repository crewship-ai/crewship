-- A share is a read-only capability for exactly one chat. The plaintext token
-- is returned once and never stored. Existing authenticated chat access is
-- unchanged; use-time checks also require the issuer's current authority.
CREATE TABLE chat_read_shares (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    issued_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    CHECK (expires_at > created_at)
);
CREATE INDEX idx_chat_read_shares_chat ON chat_read_shares(workspace_id, agent_id, chat_id, created_at DESC);
CREATE INDEX idx_chat_read_shares_agent_fk ON chat_read_shares(agent_id);
CREATE INDEX idx_chat_read_shares_chat_fk ON chat_read_shares(chat_id);
CREATE INDEX idx_chat_read_shares_issuer ON chat_read_shares(issued_by_user_id);
