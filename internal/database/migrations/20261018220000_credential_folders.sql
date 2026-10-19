-- Vault folders (Harbor 2, #3479). A folder groups credentials and carries the
-- policy v67 draws on its detail: who may reveal, the lowest Keeper tier a
-- member may hold, a default tier and a replace-reminder. Two levels at most
-- (parent_id points at a root folder); the API enforces the depth, the
-- partial unique indexes enforce name uniqueness per parent.
CREATE TABLE credential_folders (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  parent_id TEXT REFERENCES credential_folders(id) ON DELETE RESTRICT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  description TEXT NOT NULL DEFAULT '',
  reveal_policy TEXT NOT NULL DEFAULT 'inherit' CHECK(reveal_policy IN ('inherit','admins','nobody')),
  remind_after_days INTEGER NOT NULL DEFAULT 0 CHECK(remind_after_days >= 0),
  default_security_level INTEGER CHECK(default_security_level IS NULL OR default_security_level BETWEEN 1 AND 4),
  min_security_level INTEGER NOT NULL DEFAULT 1 CHECK(min_security_level BETWEEN 1 AND 4),
  created_by TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_credential_folders_name_root ON credential_folders(workspace_id, lower(name)) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX idx_credential_folders_name_child ON credential_folders(parent_id, lower(name)) WHERE parent_id IS NOT NULL;
CREATE INDEX idx_credential_folders_ws ON credential_folders(workspace_id);

ALTER TABLE credentials ADD COLUMN folder_id TEXT REFERENCES credential_folders(id) ON DELETE RESTRICT;
CREATE INDEX idx_credentials_folder ON credentials(folder_id) WHERE folder_id IS NOT NULL;
