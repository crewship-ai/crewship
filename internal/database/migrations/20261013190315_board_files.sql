-- Canonical raster bytes share the tenant's content-addressed attachment store.
-- File rows remain for the Board lifetime, including unused imports and undo.
CREATE TABLE page_board_files (
 id TEXT PRIMARY KEY,
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL CHECK(length(sha256)=64),
 content_type TEXT NOT NULL CHECK(content_type IN ('image/png','image/jpeg')),
 width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 2048),
 height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 2048),
 byte_size INTEGER NOT NULL CHECK(byte_size BETWEEN 1 AND 8388608),
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX idx_board_files_board ON page_board_files(board_page_id);
CREATE INDEX idx_board_files_digest ON page_board_files(sha256);
CREATE INDEX idx_board_files_author ON page_board_files(author_user_id);
ALTER TABLE page_board_mutations ADD COLUMN result_file_id TEXT REFERENCES page_board_files(id) ON DELETE SET NULL;
CREATE INDEX idx_board_mutation_file ON page_board_mutations(result_file_id);
CREATE TRIGGER board_file_immutable BEFORE UPDATE OF board_page_id,sha256,content_type,width,height,byte_size ON page_board_files
BEGIN SELECT RAISE(ABORT,'board file is immutable'); END;
