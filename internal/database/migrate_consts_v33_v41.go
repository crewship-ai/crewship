package database

// SQL constants for migrations v33–v41 (approval gates, MCP registry
// cache, crew messaging/audit, issue tracker family). See migrate.go
// for the Migrate driver and the registry slice.

const migrationAddMCPConfigJSON = `
-- Raw .mcp.json config stored per crew (base) and per agent (additions).
-- Orchestrator merges crew + agent configs at runtime; Claude Code
-- natively expands ${VAR} references from container env vars.
ALTER TABLE crews ADD COLUMN mcp_config_json TEXT;
ALTER TABLE agents ADD COLUMN mcp_config_json TEXT;
`

// v34: AddApprovalGates
const migrationAddApprovalGates = `
-- Approval gate columns on mission tasks.
ALTER TABLE mission_tasks ADD COLUMN approval_required INTEGER DEFAULT 0;
ALTER TABLE mission_tasks ADD COLUMN approval_status TEXT;
ALTER TABLE mission_tasks ADD COLUMN approved_by TEXT;
ALTER TABLE mission_tasks ADD COLUMN approved_at TEXT;

-- Tiered escalation config per crew.
ALTER TABLE crews ADD COLUMN escalation_config TEXT;
`

// v35: AddPKCECodeVerifier
const migrationAddPKCECodeVerifier = `
ALTER TABLE oauth_states ADD COLUMN code_verifier TEXT NOT NULL DEFAULT '';
`

// v36: AddMCPRegistryCache
const migrationAddMCPRegistryCache = `
CREATE TABLE IF NOT EXISTS mcp_registry_servers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	icon TEXT NOT NULL DEFAULT '',
	transport TEXT NOT NULL DEFAULT 'stdio',
	homepage_url TEXT NOT NULL DEFAULT '',
	source_url TEXT NOT NULL DEFAULT '',
	-- For stdio servers
	package_name TEXT NOT NULL DEFAULT '',
	package_registry TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '',
	-- For remote/HTTP servers
	endpoint TEXT NOT NULL DEFAULT '',
	-- Auth info
	auth_type TEXT NOT NULL DEFAULT '',
	env_vars_json TEXT NOT NULL DEFAULT '[]',
	-- Metadata
	category TEXT NOT NULL DEFAULT '',
	is_verified INTEGER NOT NULL DEFAULT 0,
	synced_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_mcp_registry_name ON mcp_registry_servers(name);
CREATE INDEX IF NOT EXISTS idx_mcp_registry_category ON mcp_registry_servers(category);
`

// v37: AddCrewMessagingAndAudit
const migrationAddCrewMessagingAndAudit = `
CREATE TABLE IF NOT EXISTS crew_messages (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	from_crew_id TEXT NOT NULL,
	to_crew_id TEXT NOT NULL,
	from_agent_id TEXT,
	content TEXT NOT NULL,
	metadata TEXT,
	delivered_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_crew_msg_to ON crew_messages(to_crew_id, created_at);
CREATE INDEX IF NOT EXISTS idx_crew_msg_from ON crew_messages(from_crew_id, created_at);

CREATE TABLE IF NOT EXISTS crew_audit_log (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	action TEXT NOT NULL,
	from_crew_id TEXT,
	to_crew_id TEXT,
	agent_id TEXT,
	details TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_crew_audit_ws ON crew_audit_log(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_crew_audit_crew ON crew_audit_log(from_crew_id, created_at);
`

// v38: AddIssueTracker
const migrationAddIssueTracker = `
-- Extend missions with issue-tracker fields (Linear-like).
ALTER TABLE missions ADD COLUMN number INTEGER;
ALTER TABLE missions ADD COLUMN identifier TEXT;
ALTER TABLE missions ADD COLUMN priority TEXT NOT NULL DEFAULT 'none';
ALTER TABLE missions ADD COLUMN assignee_type TEXT;
ALTER TABLE missions ADD COLUMN assignee_id TEXT;
ALTER TABLE missions ADD COLUMN due_date TEXT;
ALTER TABLE missions ADD COLUMN sort_order REAL NOT NULL DEFAULT 0;
ALTER TABLE missions ADD COLUMN mission_type TEXT NOT NULL DEFAULT 'orchestration';

CREATE UNIQUE INDEX IF NOT EXISTS idx_mission_identifier ON missions(identifier) WHERE identifier IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mission_priority ON missions(priority);
CREATE INDEX IF NOT EXISTS idx_mission_type ON missions(mission_type);
CREATE INDEX IF NOT EXISTS idx_mission_sort_order ON missions(sort_order);

-- Crew issue prefix for identifiers (e.g. "ENG" -> ENG-42).
ALTER TABLE crews ADD COLUMN issue_prefix TEXT;

-- Atomic sequential counter per crew for issue numbering.
CREATE TABLE IF NOT EXISTS issue_counters (
    crew_id TEXT PRIMARY KEY REFERENCES crews(id) ON DELETE CASCADE,
    next_number INTEGER NOT NULL DEFAULT 1
);

-- Workspace-scoped labels.
CREATE TABLE IF NOT EXISTS labels (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    color TEXT NOT NULL DEFAULT '#6B7280',
    label_group TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, name)
);
CREATE INDEX IF NOT EXISTS idx_labels_workspace ON labels(workspace_id);

-- Many-to-many: missions <-> labels.
CREATE TABLE IF NOT EXISTS mission_labels (
    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    label_id TEXT NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (mission_id, label_id)
);
CREATE INDEX IF NOT EXISTS idx_mission_labels_mission ON mission_labels(mission_id);
CREATE INDEX IF NOT EXISTS idx_mission_labels_label ON mission_labels(label_id);

-- Comments on missions/issues.
CREATE TABLE IF NOT EXISTS mission_comments (
    id TEXT PRIMARY KEY,
    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    author_type TEXT NOT NULL CHECK(author_type IN ('user','agent')),
    author_id TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_mission_comments_mission ON mission_comments(mission_id);
`

// v39: AddIssueRelations
const migrationAddIssueRelations = `
-- Relations between issues (blocks, blocked_by, relates_to, duplicate_of).
CREATE TABLE IF NOT EXISTS mission_relations (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL CHECK(relation_type IN ('blocks','blocked_by','relates_to','duplicate_of')),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(source_id, target_id, relation_type)
);
CREATE INDEX IF NOT EXISTS idx_mission_rel_source ON mission_relations(source_id);
CREATE INDEX IF NOT EXISTS idx_mission_rel_target ON mission_relations(target_id);
`

// v40: AddProjects
const migrationAddProjects = `
-- Projects group issues toward a deliverable (like Linear Projects).
CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    icon TEXT,
    color TEXT NOT NULL DEFAULT '#6B7280',
    status TEXT NOT NULL DEFAULT 'planned' CHECK(status IN ('backlog','planned','in_progress','paused','completed','cancelled')),
    priority TEXT NOT NULL DEFAULT 'none' CHECK(priority IN ('none','low','medium','high','urgent')),
    health TEXT NOT NULL DEFAULT 'on_track' CHECK(health IN ('on_track','at_risk','off_track')),
    lead_type TEXT,
    lead_id TEXT,
    start_date TEXT,
    target_date TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_projects_workspace ON projects(workspace_id);
CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);

-- Link missions (issues) to projects.
ALTER TABLE missions ADD COLUMN project_id TEXT REFERENCES projects(id);
CREATE INDEX IF NOT EXISTS idx_mission_project ON missions(project_id);
`

// v41: AddIssueActivity
const migrationAddIssueActivity = `
-- Structured activity log for issues (status changes, assignments, completions).
CREATE TABLE IF NOT EXISTS mission_activity (
    id TEXT PRIMARY KEY,
    mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    actor_type TEXT NOT NULL CHECK(actor_type IN ('user','agent','system')),
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    details TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_mission_activity_mission ON mission_activity(mission_id);
CREATE INDEX IF NOT EXISTS idx_mission_activity_created ON mission_activity(created_at);
`

// v42: AddPhase2Features
