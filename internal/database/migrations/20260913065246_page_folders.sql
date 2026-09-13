-- Pages folders (docs/prd/pages-collections-access-analysis-2026-09-12.md §6,
-- issue #2527, F1). A folder is a named group of pages owned by one crew, with
-- an icon from the crew icon set and an optional colour. One level, a page in
-- at most one folder.
--
-- grants_version and pages_version are the fences a move carries (§5/8): the
-- first changes with every change to the folder's grants (none exist yet —
-- F3 — so it stays 0 until then), the second with every change to the page's
-- folder membership. A move whose fences are stale is refused with 409.
--
-- ON DELETE RESTRICT on pages.folder_id: a folder is deleted only when empty
-- (§1/9), including pages the caller cannot see, and the database enforces it
-- rather than trusting the handler.
CREATE TABLE page_folders (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug           TEXT NOT NULL,
    name           TEXT NOT NULL,
    icon           TEXT,
    color          TEXT,
    owner_crew_id  TEXT NOT NULL REFERENCES crews(id) ON DELETE RESTRICT,
    grants_version INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (workspace_id, slug)
);
CREATE INDEX idx_page_folders_owner_crew ON page_folders(owner_crew_id);

ALTER TABLE pages ADD COLUMN folder_id TEXT REFERENCES page_folders(id) ON DELETE RESTRICT;
ALTER TABLE pages ADD COLUMN pages_version INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_pages_folder ON pages(folder_id);
