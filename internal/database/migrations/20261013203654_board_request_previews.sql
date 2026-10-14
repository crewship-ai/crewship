-- Optional immutable human-supplied illustration; never canonical scene authority.
CREATE TABLE page_board_request_previews (
 request_id TEXT NOT NULL PRIMARY KEY REFERENCES page_board_requests(id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL CHECK(length(sha256)=64),
 width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 1024),
 height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 1024),
 byte_size INTEGER NOT NULL CHECK(byte_size BETWEEN 1 AND 2097152),
 created_at TEXT NOT NULL
);
CREATE INDEX idx_board_request_preview_digest ON page_board_request_previews(sha256);
CREATE TRIGGER board_request_preview_immutable BEFORE UPDATE ON page_board_request_previews
BEGIN SELECT RAISE(ABORT,'immutable board request illustration'); END;
