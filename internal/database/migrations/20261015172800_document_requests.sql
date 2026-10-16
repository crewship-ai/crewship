CREATE TABLE document_requests (
 id TEXT PRIMARY KEY, page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 base_rev INTEGER NOT NULL, submission_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
 instruction TEXT NOT NULL, receipt_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed')),
 run_id TEXT, result_revision INTEGER, checkpoint_id TEXT, error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(page_id,human_user_id,submission_id)
);
CREATE INDEX idx_document_requests_dispatch ON document_requests(status,created_at);

-- Retain completed history and fence work whose dispatch identity disappears.
CREATE TRIGGER document_requests_human_user_id_deleted BEFORE DELETE ON users
BEGIN
 UPDATE document_requests SET
  error=CASE WHEN status IN ('queued','running') THEN 'Agent work stopped because its dispatch identity was deleted; send a new request to run again' ELSE error END,
  updated_at=CASE WHEN status IN ('queued','running') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE updated_at END,
  run_id=CASE WHEN status IN ('queued','running') THEN NULL ELSE run_id END,
  receipt_json='{}',
  status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END
 WHERE human_user_id=OLD.id;
END;

-- Retain completed history and fence work whose dispatch identity disappears.
CREATE TRIGGER document_requests_chat_id_deleted BEFORE DELETE ON chats
BEGIN
 UPDATE document_requests SET
  error=CASE WHEN status IN ('queued','running') THEN 'Agent work stopped because its dispatch identity was deleted; send a new request to run again' ELSE error END,
  updated_at=CASE WHEN status IN ('queued','running') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE updated_at END,
  run_id=CASE WHEN status IN ('queued','running') THEN NULL ELSE run_id END,
  receipt_json='{}',
  status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END
 WHERE chat_id=OLD.id;
END;

-- Retain completed history and fence work whose dispatch identity disappears.
CREATE TRIGGER document_requests_agent_id_deleted BEFORE DELETE ON agents
BEGIN
 UPDATE document_requests SET
  error=CASE WHEN status IN ('queued','running') THEN 'Agent work stopped because its dispatch identity was deleted; send a new request to run again' ELSE error END,
  updated_at=CASE WHEN status IN ('queued','running') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE updated_at END,
  run_id=CASE WHEN status IN ('queued','running') THEN NULL ELSE run_id END,
  receipt_json='{}',
  status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END
 WHERE agent_id=OLD.id;
END;
