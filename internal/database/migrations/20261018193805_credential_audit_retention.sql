-- Credential retirement must not erase audit history. Keep all existing
-- columns and rows, including nullable attribution, timestamps and metadata.
-- Workspace erasure and configured audit retention remain separate operations.
CREATE TABLE credential_audit_retained (
    id TEXT PRIMARY KEY,
    credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE NO ACTION,
    event_type TEXT NOT NULL,
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    ip_address TEXT,
    metadata_json TEXT,
    occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE
);
INSERT INTO credential_audit_retained
    (id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at, workspace_id)
SELECT id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at, workspace_id
FROM credential_audit;
DROP TABLE credential_audit;
ALTER TABLE credential_audit_retained RENAME TO credential_audit;
CREATE INDEX idx_credential_audit_credential ON credential_audit(credential_id, occurred_at DESC);
CREATE INDEX idx_credential_audit_occurred ON credential_audit(occurred_at);
CREATE INDEX idx_credential_audit_workspace_time ON credential_audit(workspace_id, occurred_at DESC);
CREATE INDEX idx_credential_audit_agent ON credential_audit(agent_id);

-- Preserve whole-workspace hard erasure, including pre-attribution audit rows
-- whose workspace_id is NULL. This never runs for ordinary soft retirement.
CREATE TRIGGER erase_workspace_credential_history
BEFORE DELETE ON workspaces
BEGIN
    DELETE FROM credential_audit
    WHERE credential_id IN (SELECT id FROM credentials WHERE workspace_id = OLD.id);
END;
