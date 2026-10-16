-- Identity deletion triggers and FK actions must locate retained requests without
-- scanning every document's history.
CREATE INDEX idx_document_requests_human ON document_requests(human_user_id);
CREATE INDEX idx_document_requests_chat ON document_requests(chat_id);
CREATE INDEX idx_document_requests_agent ON document_requests(agent_id);
