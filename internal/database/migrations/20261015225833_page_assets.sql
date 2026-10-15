CREATE TABLE page_assets (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL CHECK(length(sha256)=64),
 content_type TEXT NOT NULL CHECK(content_type IN ('image/png','image/jpeg')),
 width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 2048),
 height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 2048),
 byte_size INTEGER NOT NULL CHECK(byte_size BETWEEN 1 AND 8388608),
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'staged' CHECK(state IN ('staged','retained')),
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 UNIQUE(page_id,mutation_id)
);
CREATE INDEX idx_page_assets_digest ON page_assets(sha256);
CREATE INDEX idx_page_assets_expiry ON page_assets(state,expires_at);
CREATE TRIGGER page_asset_immutable BEFORE UPDATE OF page_id,sha256,content_type,width,height,byte_size,mutation_id,request_digest,created_at,expires_at ON page_assets
BEGIN SELECT RAISE(ABORT,'page asset identity is immutable'); END;
CREATE TRIGGER page_asset_retention_monotonic BEFORE UPDATE OF state ON page_assets
WHEN OLD.state='retained' AND NEW.state!='retained'
BEGIN SELECT RAISE(ABORT,'retained page asset cannot become staged'); END;
