-- Human-authored static HTML snapshots. Source remains immutable in workspace
-- content-addressed attachment storage; every historical revision pins its blob.
CREATE TABLE page_artifacts (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 head_rev INTEGER NOT NULL CHECK(head_rev > 0)
);
CREATE TABLE page_artifact_revisions (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision > 0),
 sha256 TEXT NOT NULL CHECK(length(sha256)=64),
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 restored_from INTEGER,
 created_at TEXT NOT NULL,
 UNIQUE(page_id,revision),
 UNIQUE(page_id,mutation_id)
);
CREATE INDEX idx_page_artifact_blobs ON page_artifact_revisions(sha256);
-- Enforce the type boundary even when a concurrent legacy writer has already
-- passed its application-level checks. A failed transaction leaves both intact.
CREATE TRIGGER page_artifact_empty_page BEFORE INSERT ON page_artifacts
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'artifact requires an empty human-owned page'); END;
CREATE TRIGGER page_artifact_no_panels BEFORE INSERT ON page_panels
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'artifact pages cannot contain panels'); END;
CREATE TRIGGER page_artifact_no_project BEFORE INSERT ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'artifact pages cannot contain applications'); END;
CREATE TRIGGER page_artifact_human_owner BEFORE UPDATE OF owner_user_id,owner_crew_id ON pages
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.id) AND (NEW.owner_user_id IS NULL OR NEW.owner_crew_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'artifact owner must be human'); END;
