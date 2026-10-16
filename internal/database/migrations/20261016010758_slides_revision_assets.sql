CREATE UNIQUE INDEX idx_page_assets_page_identity_digest ON page_assets(page_id,id,sha256);
CREATE TABLE slides_revision_assets (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 rev INTEGER NOT NULL,
 asset_id TEXT NOT NULL,
 expected_sha256 TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id,rev,asset_id),
 FOREIGN KEY(workspace_id,page_id,rev) REFERENCES slides_revisions(workspace_id,page_id,rev) ON DELETE CASCADE,
 FOREIGN KEY(page_id,asset_id,expected_sha256) REFERENCES page_assets(page_id,id,sha256) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX idx_slides_revision_assets_asset ON slides_revision_assets(page_id,asset_id,expected_sha256);
CREATE INDEX idx_slides_revision_assets_digest ON slides_revision_assets(expected_sha256,page_id,asset_id);
CREATE TRIGGER slides_revision_assets_immutable BEFORE UPDATE ON slides_revision_assets
BEGIN SELECT RAISE(ABORT,'Slides revision asset references are immutable'); END;
CREATE TRIGGER slides_revision_assets_no_delete BEFORE DELETE ON slides_revision_assets
WHEN EXISTS(SELECT 1 FROM slides_revisions WHERE workspace_id=OLD.workspace_id AND page_id=OLD.page_id AND rev=OLD.rev)
BEGIN SELECT RAISE(ABORT,'Slides asset references are retained with their revision'); END;
