-- What a workspace is for. A sign-up with nothing to redeem gets an empty
-- "<Name>'s Workspace" of its own (auth.go), and on an instance where people
-- sign up and are then invited, those pile up next to the workspaces people
-- actually share: Admin › People & workspaces could not tell them apart.
--
--   team      a workspace people share — created by an admin, the first
--             account, or anyone through the workspace API. The default.
--   personal  created at sign-up for that one person.
--
-- An instance admin can change it (PATCH /admin/instance/workspaces/{id}),
-- for a personal workspace that grew into a team one.
ALTER TABLE workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT 'team' CHECK (kind IN ('team', 'personal'));

-- Backfill: the sign-up workspace still looks exactly as sign-up made it —
-- its one member is its owner, and it is named after them. A renamed or
-- shared one stays a team workspace, which is what it has become.
UPDATE workspaces SET kind = 'personal'
WHERE id IN (
    SELECT w.id
    FROM workspaces w
    JOIN workspace_members m ON m.workspace_id = w.id AND m.role = 'OWNER'
    JOIN users u ON u.id = m.user_id
    WHERE w.name = COALESCE(u.full_name, '') || '''s Workspace'
      AND (SELECT COUNT(*) FROM workspace_members x WHERE x.workspace_id = w.id) = 1
);
