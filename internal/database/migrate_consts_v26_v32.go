package database

// SQL constants for migrations v26–v32. See migrate.go for the
// Migrate driver and the registry slice. Each const corresponds to
// a `{version: N, name: ..., sql: <name>}` entry in `migrations`.

// v26: AddCrewTemplatesWorkspaceID
const migrationAddCrewTemplatesWorkspaceID = `
ALTER TABLE crew_templates ADD COLUMN workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_crew_templates_workspace ON crew_templates(workspace_id);
`

// v27: AddEscalationAction
const migrationAddEscalationAction = `
ALTER TABLE escalations ADD COLUMN action TEXT DEFAULT 'approve';
ALTER TABLE escalations ADD COLUMN redirect_to TEXT;
`

// v28: AddTaskScalingAndHandoff
const migrationAddTaskScalingAndHandoff = `
-- Mission-level orchestration metadata
ALTER TABLE missions ADD COLUMN total_token_budget INTEGER;
ALTER TABLE missions ADD COLUMN complexity TEXT DEFAULT 'MEDIUM';
ALTER TABLE missions ADD COLUMN pattern TEXT DEFAULT 'ORCHESTRATOR';

-- Task-level scaling, budget, and handoff tracking
ALTER TABLE mission_tasks ADD COLUMN complexity TEXT DEFAULT 'MEDIUM';
ALTER TABLE mission_tasks ADD COLUMN token_budget INTEGER DEFAULT 50000;
ALTER TABLE mission_tasks ADD COLUMN tokens_used INTEGER DEFAULT 0;
ALTER TABLE mission_tasks ADD COLUMN tool_calls_count INTEGER DEFAULT 0;
ALTER TABLE mission_tasks ADD COLUMN tool_calls_budget INTEGER DEFAULT 15;
ALTER TABLE mission_tasks ADD COLUMN confidence REAL;
ALTER TABLE mission_tasks ADD COLUMN needs_review INTEGER DEFAULT 0;
ALTER TABLE mission_tasks ADD COLUMN handoff_context TEXT;
ALTER TABLE mission_tasks ADD COLUMN evaluation_status TEXT;
ALTER TABLE mission_tasks ADD COLUMN evaluation_notes TEXT;
ALTER TABLE mission_tasks ADD COLUMN retry_count INTEGER DEFAULT 0;
ALTER TABLE mission_tasks ADD COLUMN priority INTEGER DEFAULT 3;
ALTER TABLE mission_tasks ADD COLUMN labels TEXT;
`

// v29: AddMCPGateway
const migrationAddMCPGateway = `
-- Workspace-level MCP server integrations (shared across all crews)
CREATE TABLE workspace_mcp_servers (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name TEXT NOT NULL,
	display_name TEXT NOT NULL,
	transport TEXT NOT NULL DEFAULT 'streamable-http',
	endpoint TEXT,
	command TEXT,
	args_json TEXT,
	env_json TEXT,
	config_json TEXT,
	icon TEXT,
	enabled INTEGER DEFAULT 1,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(workspace_id, name)
);
CREATE INDEX idx_ws_mcp_workspace ON workspace_mcp_servers(workspace_id);

-- Crew-level MCP server integrations (override or extend workspace)
CREATE TABLE crew_mcp_servers (
	id TEXT PRIMARY KEY,
	crew_id TEXT NOT NULL REFERENCES crews(id),
	workspace_mcp_server_id TEXT REFERENCES workspace_mcp_servers(id),
	name TEXT NOT NULL,
	display_name TEXT NOT NULL,
	transport TEXT NOT NULL DEFAULT 'streamable-http',
	endpoint TEXT,
	command TEXT,
	args_json TEXT,
	env_json TEXT,
	config_json TEXT,
	icon TEXT,
	enabled INTEGER DEFAULT 1,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(crew_id, name)
);
CREATE INDEX idx_crew_mcp_crew ON crew_mcp_servers(crew_id);

-- Per-agent MCP binding with credential assignment
CREATE TABLE agent_mcp_bindings (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id),
	mcp_server_id TEXT NOT NULL,
	mcp_server_scope TEXT NOT NULL CHECK(mcp_server_scope IN ('workspace','crew')),
	credential_id TEXT REFERENCES credentials(id),
	enabled INTEGER DEFAULT 1,
	config_override_json TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(agent_id, mcp_server_id, mcp_server_scope)
);
CREATE INDEX idx_agent_mcp_agent ON agent_mcp_bindings(agent_id);

-- Audit log for MCP tool calls
CREATE TABLE mcp_tool_calls (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	crew_id TEXT,
	agent_id TEXT NOT NULL,
	mcp_server_id TEXT NOT NULL,
	mcp_server_scope TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	input_hash TEXT,
	status TEXT NOT NULL CHECK(status IN ('success','error','denied','timeout')),
	duration_ms INTEGER,
	error_message TEXT,
	session_id TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_mcp_calls_ws ON mcp_tool_calls(workspace_id, created_at);
CREATE INDEX idx_mcp_calls_agent ON mcp_tool_calls(agent_id, created_at);
`

// v30: FixMCPGatewayConstraints
const migrationFixMCPGatewayConstraints = `
-- Recreate workspace_mcp_servers with ON DELETE CASCADE
CREATE TABLE workspace_mcp_servers_new (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	display_name TEXT NOT NULL,
	transport TEXT NOT NULL DEFAULT 'streamable-http',
	endpoint TEXT,
	command TEXT,
	args_json TEXT,
	env_json TEXT,
	config_json TEXT,
	icon TEXT,
	enabled INTEGER DEFAULT 1,
	deleted_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(workspace_id, name)
);
INSERT INTO workspace_mcp_servers_new (id, workspace_id, name, display_name, transport, endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at)
	SELECT id, workspace_id, name, display_name, transport, endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at FROM workspace_mcp_servers;
DROP TABLE workspace_mcp_servers;
ALTER TABLE workspace_mcp_servers_new RENAME TO workspace_mcp_servers;
CREATE INDEX idx_ws_mcp_workspace ON workspace_mcp_servers(workspace_id);

-- Recreate crew_mcp_servers with ON DELETE CASCADE
CREATE TABLE crew_mcp_servers_new (
	id TEXT PRIMARY KEY,
	crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
	workspace_mcp_server_id TEXT REFERENCES workspace_mcp_servers(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	display_name TEXT NOT NULL,
	transport TEXT NOT NULL DEFAULT 'streamable-http',
	endpoint TEXT,
	command TEXT,
	args_json TEXT,
	env_json TEXT,
	config_json TEXT,
	icon TEXT,
	enabled INTEGER DEFAULT 1,
	deleted_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(crew_id, name)
);
INSERT INTO crew_mcp_servers_new (id, crew_id, workspace_mcp_server_id, name, display_name, transport, endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at)
	SELECT id, crew_id, workspace_mcp_server_id, name, display_name, transport, endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at FROM crew_mcp_servers;
DROP TABLE crew_mcp_servers;
ALTER TABLE crew_mcp_servers_new RENAME TO crew_mcp_servers;
CREATE INDEX idx_crew_mcp_crew ON crew_mcp_servers(crew_id);
CREATE INDEX idx_crew_mcp_ws_server ON crew_mcp_servers(workspace_mcp_server_id);

-- Recreate agent_mcp_bindings with ON DELETE CASCADE / SET NULL + cred_type/cred_header
CREATE TABLE agent_mcp_bindings_new (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	mcp_server_id TEXT NOT NULL,
	mcp_server_scope TEXT NOT NULL CHECK(mcp_server_scope IN ('workspace','crew')),
	credential_id TEXT REFERENCES credentials(id) ON DELETE SET NULL,
	cred_type TEXT DEFAULT 'bearer',
	cred_header TEXT,
	enabled INTEGER DEFAULT 1,
	config_override_json TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(agent_id, mcp_server_id, mcp_server_scope)
);
INSERT INTO agent_mcp_bindings_new (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, config_override_json, created_at)
	SELECT id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, config_override_json, created_at FROM agent_mcp_bindings;
DROP TABLE agent_mcp_bindings;
ALTER TABLE agent_mcp_bindings_new RENAME TO agent_mcp_bindings;
CREATE INDEX idx_agent_mcp_agent ON agent_mcp_bindings(agent_id);
CREATE INDEX idx_agent_mcp_server ON agent_mcp_bindings(mcp_server_id, mcp_server_scope);

-- Add missing indexes on mcp_tool_calls
CREATE INDEX IF NOT EXISTS idx_mcp_calls_server ON mcp_tool_calls(mcp_server_id, created_at);

-- Trigger: validate polymorphic FK on agent_mcp_bindings
CREATE TRIGGER IF NOT EXISTS trg_agent_mcp_binding_fk_check
BEFORE INSERT ON agent_mcp_bindings
BEGIN
	SELECT RAISE(ABORT, 'mcp_server_id not found in referenced table')
	WHERE (NEW.mcp_server_scope = 'workspace'
		AND NOT EXISTS (SELECT 1 FROM workspace_mcp_servers WHERE id = NEW.mcp_server_id AND deleted_at IS NULL))
	   OR (NEW.mcp_server_scope = 'crew'
		AND NOT EXISTS (SELECT 1 FROM crew_mcp_servers WHERE id = NEW.mcp_server_id AND deleted_at IS NULL));
END;
`

// v31: AddMCPBindingEnvVar
const migrationAddMCPBindingEnvVar = `
-- Env var name for stdio MCP credential injection (e.g. GITHUB_TOKEN, SLACK_TOKEN)
ALTER TABLE agent_mcp_bindings ADD COLUMN env_var_name TEXT;
`

// v32: AddOAuthCredentials
const migrationAddOAuthCredentials = `
-- OAuth 2.0 credential fields (extends existing credentials table)
ALTER TABLE credentials ADD COLUMN oauth_client_id TEXT;
ALTER TABLE credentials ADD COLUMN oauth_client_secret_enc TEXT;
ALTER TABLE credentials ADD COLUMN oauth_auth_url TEXT;
ALTER TABLE credentials ADD COLUMN oauth_token_url TEXT;
ALTER TABLE credentials ADD COLUMN oauth_scopes TEXT;
ALTER TABLE credentials ADD COLUMN oauth_refresh_token_enc TEXT;
ALTER TABLE credentials ADD COLUMN oauth_token_expires_at TEXT;

-- OAuth state tokens for CSRF protection during auth flow
CREATE TABLE IF NOT EXISTS oauth_states (
	state TEXT PRIMARY KEY,
	credential_id TEXT NOT NULL,
	workspace_id TEXT NOT NULL,
	redirect_uri TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// v33: AddMCPConfigJSON
