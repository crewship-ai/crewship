CREATE TABLE page_artifact_requests (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 thread_id TEXT,
 base_rev INTEGER NOT NULL,
 submission_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 instruction TEXT NOT NULL,
 snapshot_json TEXT NOT NULL,
 receipt_json TEXT NOT NULL,
 next_attempt_at TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 run_id TEXT,
 cancelled_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 result_revision INTEGER,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(page_id,human_user_id,submission_id)
);
CREATE INDEX idx_artifact_request_human ON page_artifact_requests(human_user_id);
CREATE INDEX idx_artifact_request_chat ON page_artifact_requests(chat_id);
CREATE INDEX idx_artifact_request_agent ON page_artifact_requests(agent_id);
CREATE INDEX idx_artifact_request_queue ON page_artifact_requests(status,next_attempt_at,created_at);
ALTER TABLE page_artifact_run_delegations ADD COLUMN request_id TEXT REFERENCES page_artifact_requests(id) ON DELETE CASCADE;
CREATE INDEX idx_artifact_delegation_request ON page_artifact_run_delegations(request_id);
CREATE INDEX idx_artifact_request_canceller ON page_artifact_requests(cancelled_by_user_id);
