-- Human collaboration is independent of agent sessions and their lifecycle.
CREATE TABLE workspace_conversations (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('channel','group')),
 title TEXT NOT NULL,
 created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 last_sequence INTEGER NOT NULL DEFAULT 0,
 deleted_at TEXT
);
CREATE INDEX idx_workspace_conversations_recent ON workspace_conversations(workspace_id,updated_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE TABLE workspace_conversation_members (
 conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 joined_at TEXT NOT NULL,
 last_read_sequence INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(conversation_id,user_id)
);
CREATE INDEX idx_workspace_conversation_members_user ON workspace_conversation_members(user_id,conversation_id);
CREATE TABLE workspace_conversation_messages (
 id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 sequence INTEGER NOT NULL,
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 client_id TEXT NOT NULL,
 content TEXT NOT NULL,
 created_at TEXT NOT NULL,
 CHECK(author_user_id IS NULL OR author_agent_id IS NULL),
 UNIQUE(conversation_id,sequence),
 UNIQUE(conversation_id,author_user_id,client_id)
);
CREATE TABLE workspace_conversation_outbox (
 id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 message_id TEXT NOT NULL UNIQUE REFERENCES workspace_conversation_messages(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 delivered_at TEXT
);
CREATE INDEX idx_workspace_conversation_outbox_pending ON workspace_conversation_outbox(created_at,id) WHERE delivered_at IS NULL;
