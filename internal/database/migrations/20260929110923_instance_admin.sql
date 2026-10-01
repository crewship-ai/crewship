-- Instance administration, separate from any workspace role.
--
-- Until now every instance-wide power (rate limits, log level, Keeper models,
-- notification providers, instance settings, flag definitions) reduced to
-- OWNER/ADMIN of whichever workspace the caller named, so an admin of any
-- workspace could retune the whole server. instance_role names the people
-- who administer the instance itself. CREWSHIP_OWNER_EMAIL stays an instance
-- admin regardless of this column; with neither set, the OWNERs of the oldest
-- workspace are (see isInstanceAdmin), so no install is left without one.
ALTER TABLE users ADD COLUMN instance_role TEXT CHECK (instance_role IS NULL OR instance_role = 'ADMIN');

-- A suspended account cannot sign in, refresh, or use a CLI token; its
-- memberships stay so reactivation restores exactly what it had.
ALTER TABLE users ADD COLUMN suspended_at TEXT;
ALTER TABLE users ADD COLUMN suspended_reason TEXT;

-- Instance-level actions have no workspace, and audit_logs.workspace_id is
-- NOT NULL with ON DELETE CASCADE: deleting a workspace would delete the row
-- that records its deletion. No foreign keys here on purpose, so the trail
-- outlives the account and the workspace it names.
CREATE TABLE instance_audit_logs (
    id TEXT PRIMARY KEY,
    user_id TEXT,
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT,
    target_workspace_id TEXT,
    metadata TEXT,
    ip_address TEXT,
    user_agent TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_instance_audit_time ON instance_audit_logs(created_at);
CREATE INDEX idx_instance_audit_entity ON instance_audit_logs(entity_type, entity_id);
CREATE INDEX idx_instance_audit_user ON instance_audit_logs(user_id);
