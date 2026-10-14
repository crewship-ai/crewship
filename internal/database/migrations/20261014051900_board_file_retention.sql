-- Track when an image last participated in a confirmed scene mutation, including
-- removal. Undo can restore it for 24 hours after that mutation. Existing files
-- get a full grace window when upgrading from the lifetime-retention schema.
ALTER TABLE page_board_files ADD COLUMN last_used_at TEXT NOT NULL DEFAULT '';
UPDATE page_board_files SET last_used_at=strftime('%Y-%m-%dT%H:%M:%fZ','now');
CREATE INDEX idx_board_file_retention ON page_board_files(board_page_id,last_used_at);
