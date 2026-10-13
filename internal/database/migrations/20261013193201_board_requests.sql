-- Human-authored immutable selection jobs. The request is durable history;
-- receipt_json and running/queued execution must be suspended on restore.
CREATE TABLE page_board_requests (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 board_page_id TEXT REFERENCES page_boards(page_id) ON DELETE SET NULL,
 original_board_id TEXT NOT NULL,
 human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
 kind TEXT NOT NULL CHECK(kind IN ('diagram','artifact')),
 base_seq INTEGER NOT NULL,
 submission_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 instruction TEXT NOT NULL,
 selected_element_ids_json TEXT NOT NULL,
 selected_placement_ids_json TEXT NOT NULL,
 snapshot_json TEXT NOT NULL,
 target_json TEXT NOT NULL,
 dialect TEXT NOT NULL,
 converter_version TEXT NOT NULL,
 receipt_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','ready_to_insert','inserted','succeeded','failed','cancelled')),
 next_attempt_at TEXT NOT NULL DEFAULT '',
 run_id TEXT,
 mermaid_source TEXT NOT NULL DEFAULT '',
 source_hash TEXT NOT NULL DEFAULT '',
 source_mutation_id TEXT NOT NULL DEFAULT '',
 source_fingerprint TEXT NOT NULL DEFAULT '',
 result_seq INTEGER,
 result_page_id TEXT,
 result_revision INTEGER,
 inserted_element_ids_json TEXT NOT NULL DEFAULT '[]',
 inserted_element_hashes_json TEXT NOT NULL DEFAULT '{}',
 inserted_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 cancelled_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 pin_status TEXT NOT NULL DEFAULT '',
 pin_reason TEXT NOT NULL DEFAULT '',
 result_placement_id TEXT,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(board_page_id,human_user_id,submission_id),
 FOREIGN KEY(result_page_id,result_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL,
 CHECK((result_page_id IS NULL AND result_revision IS NULL) OR (result_page_id IS NOT NULL AND result_revision>0))
);
CREATE INDEX idx_board_request_workspace ON page_board_requests(workspace_id);
CREATE INDEX idx_board_request_human ON page_board_requests(human_user_id);
CREATE INDEX idx_board_request_agent ON page_board_requests(agent_id);
CREATE INDEX idx_board_request_chat ON page_board_requests(chat_id);
CREATE INDEX idx_board_request_queue ON page_board_requests(status,next_attempt_at,created_at);
CREATE INDEX idx_board_request_run ON page_board_requests(run_id);
CREATE INDEX idx_board_request_inserted_by ON page_board_requests(inserted_by_user_id);
CREATE INDEX idx_board_request_cancelled_by ON page_board_requests(cancelled_by_user_id);
CREATE INDEX idx_board_request_result ON page_board_requests(result_page_id,result_revision);
CREATE INDEX idx_board_request_revision ON page_board_requests(result_revision);
CREATE TABLE page_board_request_files (
 request_id TEXT NOT NULL REFERENCES page_board_requests(id) ON DELETE CASCADE,
 file_id TEXT NOT NULL REFERENCES page_board_files(id) ON DELETE CASCADE,
 PRIMARY KEY(request_id,file_id)
);
CREATE INDEX idx_board_request_file ON page_board_request_files(file_id);
CREATE TABLE page_board_request_sources (
 id TEXT PRIMARY KEY,
 request_id TEXT NOT NULL REFERENCES page_board_requests(id) ON DELETE CASCADE,
 placement_id TEXT NOT NULL,
 source_page_id TEXT,
 source_revision INTEGER,
 FOREIGN KEY(source_page_id,source_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL,
 CHECK((source_page_id IS NULL AND source_revision IS NULL) OR (source_page_id IS NOT NULL AND source_revision>0)),
 UNIQUE(request_id,placement_id)
);
CREATE INDEX idx_board_request_source_page ON page_board_request_sources(source_page_id,source_revision);
CREATE INDEX idx_board_request_source_revision ON page_board_request_sources(source_revision);
ALTER TABLE artifact_creation_authorizations ADD COLUMN board_request_id TEXT REFERENCES page_board_requests(id) ON DELETE CASCADE;
CREATE INDEX idx_artifact_creation_board_request ON artifact_creation_authorizations(board_request_id);
ALTER TABLE page_board_elements ADD COLUMN origin_request_id TEXT REFERENCES page_board_requests(id) ON DELETE SET NULL;
CREATE INDEX idx_board_element_origin_request ON page_board_elements(origin_request_id);
