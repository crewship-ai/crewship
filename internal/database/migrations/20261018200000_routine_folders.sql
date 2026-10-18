-- Routine folders (ODCH-RT-01, Harbor 2 Routines panel "Group by Folder").
-- A folder is a named, optionally coloured group of routines in one
-- workspace. A routine is in at most one folder (pipeline_id is the primary
-- key of the membership table); deleting a folder files its routines back
-- under "No folder" and never deletes a routine. Names are unique per
-- workspace, ignoring case.
--
-- Membership is its own table rather than a column on pipelines: the routine
-- row is the hot, versioned source of truth, and organising the panel must not
-- change its shape.
--
-- Not here: sharing a folder with crews or people ("access from the folder").
-- That is a separate ACL, see docs/guides/routines.mdx.
CREATE TABLE routine_folders (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    color        TEXT,
    created_by   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_routine_folders_name ON routine_folders(workspace_id, lower(name));

CREATE TABLE routine_folder_members (
    pipeline_id  TEXT PRIMARY KEY REFERENCES pipelines(id) ON DELETE CASCADE,
    folder_id    TEXT NOT NULL REFERENCES routine_folders(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE INDEX idx_routine_folder_members_folder ON routine_folder_members(folder_id);
