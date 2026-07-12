package database

// migrationKeeperGovernanceSettings (v137) stores the per-workspace Keeper
// watchdog governance settings so OWNER/ADMIN can toggle the behavioral
// watchdog and route its findings from the dashboard instead of editing the
// server's env (issue #1001, M0).
//
// One row per workspace (workspace_id PK). No row means the behavioral
// watchdog is OFF for that workspace — it is opt-in and only runs once an
// OWNER/ADMIN enables it (the credential-access gatekeeper enforcement stays
// server-configured and is unaffected). security_contact_user_id targets the snitch
// inbox items at a named admin (empty = the legacy TargetRole MANAGER
// fanout). deny_notify_min_risk is the risk score (1–10) at or above which a
// DENY decision also lands in the inbox — ESCALATE always does.
const migrationKeeperGovernanceSettings = `
CREATE TABLE IF NOT EXISTS keeper_governance_settings (
    workspace_id             TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled                  INTEGER NOT NULL DEFAULT 0,
    security_contact_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    deny_notify_min_risk     INTEGER NOT NULL DEFAULT 7,
    updated_by               TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
`
