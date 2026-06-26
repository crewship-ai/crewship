package database

// migrationComposioDefaults (v117) adds the default-connector columns to
// composio_settings (PRD managed-integrations: COMPOSIO_DEFAULT_CONNECTOR).
//
// When the COMPOSIO_DEFAULT_CONNECTOR flag is ON, every agent that lacks an
// explicit per-agent Composio binding gets a workspace-wide DEFAULT Composio
// MCP server granting full access to all the workspace's connected apps. The
// two columns below pin which Composio user the default is scoped to and which
// provisioned Composio MCP server backs it:
//
//   - default_user_id        NULL = no default configured yet. Set explicitly
//     (`composio default set <user_id>`) or auto-derived when exactly one
//     Composio user has connected accounts.
//   - default_mcp_server_id  NULL = not provisioned yet. The id of the
//     find-or-created Composio MCP server (`crewship-<ws8>-default`) the
//     runtime resolver builds the per-user transport URL from.
//
// Both NULL keeps today's behaviour (no default injected), so the flag is the
// only thing that turns the feature on. ADD COLUMN is SQLite-safe and the
// _migrations version guard makes the whole migration run exactly once.
const migrationComposioDefaults = `
ALTER TABLE composio_settings ADD COLUMN default_user_id TEXT;
ALTER TABLE composio_settings ADD COLUMN default_mcp_server_id TEXT;
`
