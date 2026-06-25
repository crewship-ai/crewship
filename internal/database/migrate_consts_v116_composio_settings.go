package database

// migrationComposioSettings (v116) stores the per-workspace Composio
// managed-integration provider config so operators can set the project API key
// from the dashboard instead of editing the server's .env.local.
//
// One row per workspace (workspace_id PK). The API key is stored ENCRYPTED via
// internal/encryption (AES-GCM, same scheme as credentials) — a DB dump alone
// can't read it without ENCRYPTION_KEY. base_url is retained for schema
// compatibility but is always stored empty — the host is never client-supplied
// (SSRF); only the server env COMPOSIO_BASE_URL may pin a non-default host.
// label is a human-friendly project name shown in the UI. The ComposioHandler
// resolves the effective key per
// request: workspace row first, then the server COMPOSIO_API_KEY env fallback.
const migrationComposioSettings = `
CREATE TABLE IF NOT EXISTS composio_settings (
    workspace_id      TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    encrypted_api_key TEXT NOT NULL,
    base_url          TEXT,
    label             TEXT,
    created_by        TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
`
