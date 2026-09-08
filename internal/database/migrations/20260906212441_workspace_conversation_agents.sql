-- Agents join workspace-readable channels explicitly before receiving history.
-- Private groups cannot dispatch into workspace-readable legacy assignments.
ALTER TABLE workspace_conversation_messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'message' CHECK(kind IN ('message','agent_joined','agent_left'));
ALTER TABLE workspace_conversation_messages ADD COLUMN subject_agent_id TEXT;
ALTER TABLE workspace_conversation_messages ADD COLUMN mentioned_agent_ids_json TEXT NOT NULL DEFAULT '[]';
CREATE UNIQUE INDEX idx_workspace_conversation_agent_client ON workspace_conversation_messages(conversation_id,author_agent_id,client_id) WHERE author_agent_id IS NOT NULL;
CREATE TABLE workspace_conversation_agents (
 conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 joined_by TEXT REFERENCES users(id) ON DELETE SET NULL,
 joined_at TEXT NOT NULL,
 PRIMARY KEY(conversation_id,agent_id)
);
CREATE TABLE workspace_conversation_agent_jobs (
 id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 message_id TEXT NOT NULL REFERENCES workspace_conversation_messages(id) ON DELETE CASCADE,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 requested_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','queued','completed','failed')),
 assignment_id TEXT,
 reply_message_id TEXT REFERENCES workspace_conversation_messages(id) ON DELETE SET NULL,
 error TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(message_id,agent_id)
);
CREATE INDEX idx_workspace_conversation_agent_jobs_pending ON workspace_conversation_agent_jobs(updated_at,id) WHERE state IN ('pending','queued');

CREATE UNIQUE INDEX idx_workspace_conversation_agent_assignment ON workspace_conversation_agent_jobs(assignment_id) WHERE assignment_id IS NOT NULL;
