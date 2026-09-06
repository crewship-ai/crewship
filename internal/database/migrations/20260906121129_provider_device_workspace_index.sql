-- Workspace-scoped device login lookups and workspace deletion must not scan
-- every pending authorization on the instance.
CREATE INDEX IF NOT EXISTS idx_provider_device_logins_workspace
    ON provider_device_logins (workspace_id);
