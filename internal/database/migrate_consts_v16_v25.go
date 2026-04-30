package database

// SQL constants for migrations v16–v25: agent CLI tools, credential
// crew bindings, network policy, workflow templates, crew connections,
// mission proposals, escalation type/resolve, crew templates, agent
// schedule, captain chats.

// v16: AddAgentCLITools
const migrationAddAgentCLITools = `
ALTER TABLE agents ADD COLUMN cli_tools TEXT;
`

// v17: AddCredentialCrews
const migrationAddCredentialCrews = `
CREATE TABLE IF NOT EXISTS credential_crews (
	credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
	crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	PRIMARY KEY (credential_id, crew_id)
);
CREATE INDEX IF NOT EXISTS idx_credential_crews_cred ON credential_crews(credential_id);
CREATE INDEX IF NOT EXISTS idx_credential_crews_crew ON credential_crews(crew_id);

-- Prevent cross-workspace credential-crew associations at the DB level.
CREATE TRIGGER IF NOT EXISTS trg_credential_crews_workspace_check
BEFORE INSERT ON credential_crews
BEGIN
	SELECT RAISE(ABORT, 'credential and crew must belong to the same workspace')
	WHERE (SELECT workspace_id FROM credentials WHERE id = NEW.credential_id)
	   != (SELECT workspace_id FROM crews WHERE id = NEW.crew_id);
END;

CREATE TRIGGER IF NOT EXISTS trg_credential_crews_workspace_check_upd
BEFORE UPDATE ON credential_crews
BEGIN
	SELECT RAISE(ABORT, 'credential and crew must belong to the same workspace')
	WHERE (SELECT workspace_id FROM credentials WHERE id = NEW.credential_id)
	   != (SELECT workspace_id FROM crews WHERE id = NEW.crew_id);
END;

-- Migrate existing crew-scoped credentials to junction table (same workspace only)
INSERT OR IGNORE INTO credential_crews (credential_id, crew_id, created_at)
SELECT c.id, c.crew_id, datetime('now') FROM credentials c
JOIN crews cr ON cr.id = c.crew_id AND cr.workspace_id = c.workspace_id
WHERE c.scope = 'CREW' AND c.crew_id IS NOT NULL AND c.deleted_at IS NULL AND cr.deleted_at IS NULL;
`

// v18: AddCrewNetworkPolicy
const migrationAddCrewNetworkPolicy = `
ALTER TABLE crews ADD COLUMN network_mode TEXT NOT NULL DEFAULT 'free' CHECK(network_mode IN ('free', 'restricted'));
ALTER TABLE crews ADD COLUMN allowed_domains TEXT;
`

// v19: AddWorkflowTemplates
const migrationAddWorkflowTemplates = `
CREATE TABLE IF NOT EXISTS workflow_templates (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name TEXT NOT NULL,
	description TEXT,
	template_json TEXT NOT NULL,
	icon TEXT,
	color TEXT,
	is_builtin INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_workflow_templates_ws ON workflow_templates(workspace_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_templates_name_ws ON workflow_templates(workspace_id, name);
`

// v20: AddCrewConnections
const migrationAddCrewConnections = `
CREATE TABLE IF NOT EXISTS crew_connections (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	from_crew_id TEXT NOT NULL REFERENCES crews(id),
	to_crew_id TEXT NOT NULL REFERENCES crews(id),
	direction TEXT NOT NULL DEFAULT 'bidirectional' CHECK(direction IN ('unidirectional', 'bidirectional')),
	status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'inactive')),
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(from_crew_id, to_crew_id)
);
CREATE INDEX IF NOT EXISTS idx_crew_conn_from ON crew_connections(from_crew_id);
CREATE INDEX IF NOT EXISTS idx_crew_conn_to ON crew_connections(to_crew_id);
CREATE INDEX IF NOT EXISTS idx_crew_conn_ws ON crew_connections(workspace_id);
`

// v21: AddMissionProposals
const migrationAddMissionProposals = `
ALTER TABLE missions ADD COLUMN scope TEXT NOT NULL DEFAULT 'crew' CHECK(scope IN ('crew', 'workspace'));
ALTER TABLE missions ADD COLUMN proposal_id TEXT REFERENCES mission_proposals(id);

CREATE TABLE IF NOT EXISTS mission_proposals (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	proposed_by_id TEXT REFERENCES agents(id),
	title TEXT NOT NULL,
	description TEXT,
	plan TEXT,
	status TEXT NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED')),
	missions_json TEXT,
	reviewed_by TEXT,
	reviewed_at TEXT,
	review_notes TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_proposal_ws ON mission_proposals(workspace_id);
CREATE INDEX IF NOT EXISTS idx_proposal_status ON mission_proposals(status);
CREATE INDEX IF NOT EXISTS idx_mission_proposal ON missions(proposal_id);
`

// v22: AddEscalationTypeAndResolve
const migrationAddEscalationTypeAndResolve = `
-- Add type column to distinguish escalation kinds (TEXT, CREDENTIAL, LINK).
-- Default to TEXT for backwards compatibility with existing escalations.
ALTER TABLE escalations ADD COLUMN type TEXT NOT NULL DEFAULT 'TEXT' CHECK(type IN ('TEXT', 'CREDENTIAL', 'LINK'));

-- Add metadata column for structured data (e.g. link URL, credential env var name).
ALTER TABLE escalations ADD COLUMN metadata TEXT;

-- Add resolved_by to track who resolved the escalation (user/workspace member).
ALTER TABLE escalations ADD COLUMN resolved_by TEXT;
`

// v23: AddCrewTemplates
const migrationAddCrewTemplates = `
CREATE TABLE IF NOT EXISTS crew_templates (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	description TEXT,
	icon TEXT,
	color TEXT,
	category TEXT NOT NULL DEFAULT 'GENERAL',
	agents_json TEXT NOT NULL,
	is_builtin INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_crew_templates_slug ON crew_templates(slug);
CREATE INDEX IF NOT EXISTS idx_crew_templates_category ON crew_templates(category);
`

// v24: AddAgentSchedule
const migrationAddAgentSchedule = `
ALTER TABLE agents ADD COLUMN schedule_cron TEXT;
ALTER TABLE agents ADD COLUMN schedule_prompt TEXT;
ALTER TABLE agents ADD COLUMN schedule_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN schedule_last_run TEXT;
ALTER TABLE agents ADD COLUMN schedule_next_run TEXT;
`

// v25: AddCaptainChats
const migrationAddCaptainChats = `
CREATE TABLE IF NOT EXISTS captain_chats (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	messages_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_captain_chat_ws ON captain_chats(workspace_id);
CREATE INDEX IF NOT EXISTS idx_captain_chat_user ON captain_chats(user_id);
`
