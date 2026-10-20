-- Routine folders: an icon and a parent folder (#3768, the folder's own menu
-- in the Routines panel: Icon and colour…, New subfolder, Move to…).
--
-- icon is a name from the crew icon registry, the same vocabulary as page
-- folders (internal/api/pages_folder_icons.go); NULL draws the plain folder.
-- parent_id nests a folder inside another of the same workspace, at most three
-- levels deep; the API refuses a cycle. Deleting a folder hands its subfolders
-- to its own parent (internal/api/pipeline_folders.go); SET NULL is the
-- fallback when a row goes some other way, so a subfolder is never deleted
-- with its parent. Names stay unique per workspace, not per parent.
ALTER TABLE routine_folders ADD COLUMN icon TEXT;
ALTER TABLE routine_folders ADD COLUMN parent_id TEXT REFERENCES routine_folders(id) ON DELETE SET NULL;
CREATE INDEX idx_routine_folders_parent ON routine_folders(parent_id);
