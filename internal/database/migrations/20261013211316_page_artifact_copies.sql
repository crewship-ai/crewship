-- Copies are new private identities. This durable receipt is audit/provenance,
-- not authority and not a pin on the source's retained revision.
CREATE TABLE page_artifact_copies (
 id TEXT NOT NULL PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 source_page_id TEXT REFERENCES pages(id) ON DELETE SET NULL,
 source_revision INTEGER NOT NULL CHECK(source_revision>0),
 source_sha256 TEXT NOT NULL CHECK(length(source_sha256)=64),
 target_page_id TEXT UNIQUE REFERENCES pages(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(workspace_id,human_user_id,mutation_id)
);
CREATE INDEX idx_page_artifact_copies_scope ON page_artifact_copies(workspace_id,created_at);
CREATE TRIGGER page_artifact_copy_scope BEFORE INSERT ON page_artifact_copies
WHEN (NEW.source_page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.source_page_id AND workspace_id=NEW.workspace_id))
 OR (NEW.target_page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.target_page_id AND workspace_id=NEW.workspace_id))
 OR EXISTS(SELECT 1 FROM page_artifact_revisions WHERE page_id=NEW.source_page_id AND revision=NEW.source_revision AND sha256!=NEW.source_sha256)
 OR EXISTS(SELECT 1 FROM page_artifact_revisions WHERE page_id=NEW.target_page_id AND revision=1 AND (sha256!=NEW.source_sha256 OR author_user_id IS NOT NEW.human_user_id OR author_agent_id IS NOT NULL OR author_run_id IS NOT NULL))
BEGIN SELECT RAISE(ABORT,'artifact copy requires matching source and human target'); END;
CREATE TRIGGER page_artifact_copy_immutable BEFORE UPDATE ON page_artifact_copies
WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.source_revision IS NOT OLD.source_revision OR NEW.source_sha256 IS NOT OLD.source_sha256 OR NEW.mutation_id IS NOT OLD.mutation_id OR NEW.request_digest IS NOT OLD.request_digest OR NEW.created_at IS NOT OLD.created_at
 OR (NEW.human_user_id IS NOT OLD.human_user_id AND NOT(NEW.human_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.human_user_id)))
 OR (NEW.source_page_id IS NOT OLD.source_page_id AND NOT(NEW.source_page_id IS NULL AND NOT EXISTS(SELECT 1 FROM pages WHERE id=OLD.source_page_id)))
 OR (NEW.target_page_id IS NOT OLD.target_page_id AND NOT(NEW.target_page_id IS NULL AND NOT EXISTS(SELECT 1 FROM pages WHERE id=OLD.target_page_id)))
BEGIN SELECT RAISE(ABORT,'artifact copy provenance is immutable'); END;
CREATE INDEX idx_page_artifact_copies_human ON page_artifact_copies(human_user_id);
CREATE INDEX idx_page_artifact_copies_source ON page_artifact_copies(source_page_id);
