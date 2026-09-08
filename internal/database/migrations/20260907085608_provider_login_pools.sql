-- Explicit account pools; ordinary credential_bindings retain their unique
-- (workspace, scope, slot) invariant. These resources do not grant access on
-- their own. Binding/delivery integration is a separate layer.
CREATE TABLE provider_login_pools (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
    provider TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('subscription', 'api_key')),
    allow_cross_owner INTEGER NOT NULL DEFAULT 0 CHECK (allow_cross_owner IN (0, 1)),
    selection_seq INTEGER NOT NULL DEFAULT 0 CHECK (selection_seq >= 0),
    created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (workspace_id, name)
);
CREATE INDEX idx_provider_login_pools_creator ON provider_login_pools(created_by);

CREATE TABLE provider_login_pool_members (
    pool_id TEXT NOT NULL REFERENCES provider_login_pools(id) ON DELETE CASCADE,
    credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    priority INTEGER NOT NULL DEFAULT 0,
    last_selected_seq INTEGER NOT NULL DEFAULT 0 CHECK (last_selected_seq >= 0),
    PRIMARY KEY (pool_id, credential_id)
);
CREATE INDEX idx_provider_login_pool_members_credential ON provider_login_pool_members(credential_id);

-- No ciphertext or upstream messages: record only typed observations. A
-- cooldown is not a measured 5-hour / weekly usage percentage.
CREATE TABLE provider_login_availability (
    credential_id TEXT PRIMARY KEY REFERENCES credentials(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    observed_at TEXT NOT NULL,
    cooldown_until TEXT,
    blocked_reason TEXT CHECK (blocked_reason IN ('billing', 'authentication')),
    source TEXT NOT NULL CHECK (source IN ('provider_http', 'native_cli'))
);

CREATE TRIGGER trg_provider_login_pool_member_workspace_insert
BEFORE INSERT ON provider_login_pool_members
BEGIN
    SELECT RAISE(ABORT, 'provider pool workspace mismatch') WHERE NOT EXISTS (
        SELECT 1 FROM provider_login_pools p JOIN credentials c ON c.workspace_id = p.workspace_id
        WHERE p.id = NEW.pool_id AND c.id = NEW.credential_id
    );
END;
CREATE TRIGGER trg_provider_login_pool_member_workspace_update
BEFORE UPDATE OF pool_id, credential_id ON provider_login_pool_members
BEGIN
    SELECT RAISE(ABORT, 'provider pool workspace mismatch') WHERE NOT EXISTS (
        SELECT 1 FROM provider_login_pools p JOIN credentials c ON c.workspace_id = p.workspace_id
        WHERE p.id = NEW.pool_id AND c.id = NEW.credential_id
    );
END;
CREATE TRIGGER trg_provider_login_pool_workspace_update
BEFORE UPDATE OF workspace_id ON provider_login_pools
WHEN NEW.workspace_id != OLD.workspace_id
BEGIN
    SELECT RAISE(ABORT, 'provider pool workspace mismatch') WHERE EXISTS (
        SELECT 1 FROM provider_login_pool_members m JOIN credentials c ON c.id = m.credential_id
        WHERE m.pool_id = OLD.id AND c.workspace_id != NEW.workspace_id
    );
END;
CREATE TRIGGER trg_provider_login_pool_credential_workspace_update
BEFORE UPDATE OF workspace_id ON credentials
WHEN NEW.workspace_id != OLD.workspace_id
BEGIN
    SELECT RAISE(ABORT, 'provider pool workspace mismatch') WHERE EXISTS (
        SELECT 1 FROM provider_login_pool_members m JOIN provider_login_pools p ON p.id = m.pool_id
        WHERE m.credential_id = OLD.id AND p.workspace_id != NEW.workspace_id
    );
END;
