-- Explicit short-lived one-batch authority; excluded from backups.
CREATE TABLE page_board_element_permissions (
 id TEXT PRIMARY KEY,
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 elements_json TEXT NOT NULL,
 methods_json TEXT NOT NULL,
 purpose TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL,
 used_mutation_id TEXT,
 revoked_at TEXT,
 revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 UNIQUE(board_page_id,granted_by_user_id,mutation_id)
);
CREATE INDEX idx_board_permission_board ON page_board_element_permissions(board_page_id,created_at);
CREATE INDEX idx_board_permission_run ON page_board_element_permissions(run_id);
CREATE INDEX idx_board_permission_agent ON page_board_element_permissions(agent_id);
CREATE INDEX idx_board_permission_granter ON page_board_element_permissions(granted_by_user_id);
CREATE INDEX idx_board_permission_revoker ON page_board_element_permissions(revoked_by_user_id);
