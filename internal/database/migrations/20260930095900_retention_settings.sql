-- Data retention windows that have no column of their own on workspaces.
--
-- The windows that already live on workspaces (run_retention_days,
-- approvals_retention_days, audit_log_retention_days,
-- credential_audit_retention_days, page_retention_days and
-- memory_config.versions_retention_days) stay where they are; the instance
-- retention API reads and writes them in place. This table holds the rest,
-- keyed by name: today inbox_days, chats_days and keeper_decisions_days.
--
-- days NULL means keep forever. A workspace with no row for a key keeps
-- forever too, so nothing starts deleting after an upgrade: no row is
-- inserted here for any existing workspace.
--
-- updated_by is a plain user id, not a foreign key: a workspace bundle restored
-- on another instance keeps its windows even when that user does not exist
-- there.
CREATE TABLE retention_settings (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    key          TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 64),
    days         INTEGER CHECK (days IS NULL OR days BETWEEN 1 AND 3650),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_by   TEXT,
    PRIMARY KEY (workspace_id, key)
);
