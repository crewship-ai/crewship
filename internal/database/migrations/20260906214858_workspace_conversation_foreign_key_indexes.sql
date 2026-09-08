-- Conversation history, membership, jobs and the durable outbox grow with use.
-- Keep reverse identity lookups and parent cleanup proportional to the affected
-- records, rather than scanning these histories under SQLite's write lock.
CREATE INDEX idx_workspace_conversation_jobs_agent ON workspace_conversation_agent_jobs(agent_id);
CREATE INDEX idx_workspace_conversation_jobs_conversation ON workspace_conversation_agent_jobs(conversation_id);
CREATE INDEX idx_workspace_conversation_jobs_reply ON workspace_conversation_agent_jobs(reply_message_id);
CREATE INDEX idx_workspace_conversation_jobs_requester ON workspace_conversation_agent_jobs(requested_by_user_id);
CREATE INDEX idx_workspace_conversation_agents_joined_by ON workspace_conversation_agents(joined_by);
CREATE INDEX idx_workspace_conversation_messages_author_agent ON workspace_conversation_messages(author_agent_id);
CREATE INDEX idx_workspace_conversation_messages_author_user ON workspace_conversation_messages(author_user_id);
CREATE INDEX idx_workspace_conversation_outbox_conversation ON workspace_conversation_outbox(conversation_id);
CREATE INDEX idx_workspace_conversations_created_by ON workspace_conversations(created_by);

-- The membership PK starts with conversation_id: its second column cannot
-- service the reverse lookup when an agent is removed.
CREATE INDEX idx_workspace_conversation_agents_agent ON workspace_conversation_agents(agent_id);

-- The recent-list index excludes soft-deleted conversations. Parent cleanup
-- must also find those rows, so it needs an unconditional workspace index.
CREATE INDEX idx_workspace_conversations_workspace ON workspace_conversations(workspace_id);
