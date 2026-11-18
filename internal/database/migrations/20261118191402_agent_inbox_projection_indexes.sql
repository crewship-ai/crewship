-- Bound human inbox reads by recipient and workspace before evaluating
-- audience and ordering. These indexes contain no copied message bodies.
CREATE INDEX idx_agent_inbox_conversation_jobs
ON workspace_conversation_agent_jobs(agent_id,created_at DESC,id DESC) WHERE agent_id IS NOT NULL;
CREATE INDEX idx_agent_inbox_human_issues
ON missions(workspace_id,delegate_agent_id,created_by_user_id,created_at DESC,id DESC)
WHERE delegate_agent_id IS NOT NULL;
CREATE INDEX idx_agent_inbox_chat_work
ON work_items(workspace_id,agent_id,created_at DESC,id DESC)
WHERE source='chat' AND domain_kind='host_chat_turn';
