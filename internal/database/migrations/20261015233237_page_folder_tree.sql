-- Tree placement is organization only: direct leaf folder ACL remains unchanged.
-- Deferred NO ACTION permits workspace deletion and arbitrary valid restore order.
ALTER TABLE page_folders ADD COLUMN parent_id TEXT REFERENCES page_folders(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX idx_page_folders_parent ON page_folders(parent_id);
CREATE TABLE page_folder_structure (
 workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
 version INTEGER NOT NULL DEFAULT 0 CHECK(version>=0)
);
CREATE TRIGGER page_folder_parent_insert BEFORE INSERT ON page_folders
WHEN (NEW.parent_id IS NOT NULL AND (
 NEW.parent_id=NEW.id
 OR EXISTS(SELECT 1 FROM page_folders p WHERE p.id=NEW.parent_id AND p.workspace_id<>NEW.workspace_id)
 OR EXISTS(WITH RECURSIVE ancestors(id) AS (
  VALUES(NEW.parent_id) UNION SELECT f.parent_id FROM page_folders f JOIN ancestors a ON f.id=a.id WHERE f.parent_id IS NOT NULL
 ) SELECT 1 FROM ancestors WHERE id=NEW.id)
)) OR EXISTS(SELECT 1 FROM page_folders c WHERE c.parent_id=NEW.id AND c.workspace_id<>NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'invalid folder parent relation'); END;
CREATE TRIGGER page_folder_parent_update BEFORE UPDATE OF parent_id,workspace_id ON page_folders
WHEN (NEW.parent_id IS NOT NULL AND (
 NEW.parent_id=NEW.id
 OR EXISTS(SELECT 1 FROM page_folders p WHERE p.id=NEW.parent_id AND p.workspace_id<>NEW.workspace_id)
 OR EXISTS(WITH RECURSIVE ancestors(id) AS (
  VALUES(NEW.parent_id) UNION SELECT f.parent_id FROM page_folders f JOIN ancestors a ON f.id=a.id WHERE f.parent_id IS NOT NULL
 ) SELECT 1 FROM ancestors WHERE id=NEW.id)
)) OR EXISTS(SELECT 1 FROM page_folders c WHERE c.parent_id=NEW.id AND c.workspace_id<>NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'invalid folder parent relation'); END;
