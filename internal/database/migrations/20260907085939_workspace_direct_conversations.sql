-- Preserve the direct-conversation contract even after a user is deleted and
-- the pair lookup cascades away. The surviving history must not become a group
-- whose membership can be edited.
ALTER TABLE workspace_conversations ADD COLUMN is_direct INTEGER NOT NULL DEFAULT 0
 CHECK(is_direct IN (0,1) AND (is_direct=0 OR kind='group'));
CREATE TABLE workspace_conversation_direct_pairs (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 user_low_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 user_high_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 conversation_id TEXT NOT NULL UNIQUE REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 CHECK(user_low_id < user_high_id),
 PRIMARY KEY(workspace_id,user_low_id,user_high_id)
);
CREATE INDEX idx_workspace_direct_pairs_user_low ON workspace_conversation_direct_pairs(user_low_id);
CREATE INDEX idx_workspace_direct_pairs_user_high ON workspace_conversation_direct_pairs(user_high_id);
