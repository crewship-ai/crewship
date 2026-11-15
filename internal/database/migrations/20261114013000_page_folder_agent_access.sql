-- Folder access for the agents of the folder's owning crew (#5317 D2).
--
-- One setting per folder, not a page_folder_acl row: that table keeps its
-- invariant that no subject is an agent, and "one row only" holds by shape.
-- The level is at most read or propose (suggest), never write: a folder has no
-- issuer whose standing could be re-checked, so it may not hand out edits.
-- It applies only with the workspace flag artifact_agent_access_modes on, only
-- to agents whose crew owns the folder, and never to private pages.
ALTER TABLE page_folders ADD COLUMN agent_access TEXT NOT NULL DEFAULT 'none'
    CHECK (agent_access IN ('none', 'read', 'propose'));
