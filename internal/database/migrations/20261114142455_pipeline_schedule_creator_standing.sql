-- A schedule's person creator is judged at every run against the membership
-- they held when they saved it (#5339): creator_member_id is that
-- workspace_members row, so leaving and rejoining the workspace does not
-- restore their vouching. creator_set_at is when the creator was written; a
-- later revocation of the person's access (users.access_revoked_at) voids it.
-- Existing rows keep NULL and so vouch for nothing. Forward-only.
ALTER TABLE pipeline_schedules ADD COLUMN creator_member_id TEXT;
ALTER TABLE pipeline_schedules ADD COLUMN creator_set_at TEXT;
