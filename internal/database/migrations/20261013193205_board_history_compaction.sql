ALTER TABLE page_board_elements ADD COLUMN deleted_at TEXT;
-- Conservatively retain every pre-upgrade tombstone for a full history window.
UPDATE page_board_elements SET deleted_at=strftime('%Y-%m-%dT%H:%M:%f','now') || '000000Z' WHERE element_json IS NULL;
CREATE INDEX idx_board_element_deleted ON page_board_elements(board_page_id,deleted_at);
ALTER TABLE page_board_mutations ADD COLUMN element_revs_json TEXT NOT NULL DEFAULT '{}';
CREATE TABLE page_board_write_audit (
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 mutation_id TEXT NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 actor_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 actor_run_id TEXT,
 result_seq INTEGER NOT NULL,
 request_digest TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(board_page_id,mutation_id)
);
CREATE INDEX idx_board_audit_user ON page_board_write_audit(actor_user_id);
CREATE INDEX idx_board_audit_agent ON page_board_write_audit(actor_agent_id);
INSERT INTO page_board_write_audit SELECT board_page_id,mutation_id,author_user_id,author_agent_id,author_run_id,result_seq,fingerprint,created_at FROM page_board_mutations;
CREATE INDEX idx_board_mutation_created ON page_board_mutations(board_page_id,created_at);
