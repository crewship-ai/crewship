-- Run authority is ephemeral and excluded from backups. Published element
-- provenance is durable; run IDs on history remain opaque after restoration.
CREATE TABLE page_board_run_delegations (
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 granted_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 revoked_at TEXT,
 revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 PRIMARY KEY(board_page_id,run_id)
);
CREATE INDEX idx_board_delegation_run ON page_board_run_delegations(run_id);
CREATE INDEX idx_board_delegation_granter ON page_board_run_delegations(granted_by_user_id);
CREATE INDEX idx_board_delegation_revoker ON page_board_run_delegations(revoked_by_user_id);
ALTER TABLE page_board_elements ADD COLUMN author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE page_board_elements ADD COLUMN author_run_id TEXT;
CREATE INDEX idx_board_element_agent ON page_board_elements(author_agent_id);
ALTER TABLE page_board_mutations ADD COLUMN author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE page_board_mutations ADD COLUMN author_run_id TEXT;
CREATE INDEX idx_board_mutation_agent ON page_board_mutations(author_agent_id);
