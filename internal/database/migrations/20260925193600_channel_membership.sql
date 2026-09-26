-- Public channels keep workspace-wide access. Membership is independent of
-- read cursors and mute preferences, which also use this table.
-- Preserve existing participant rows; never enroll the entire workspace.
ALTER TABLE workspace_conversation_members ADD COLUMN is_member INTEGER NOT NULL DEFAULT 1 CHECK (is_member IN (0, 1));
