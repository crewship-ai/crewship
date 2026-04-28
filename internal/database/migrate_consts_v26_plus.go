package database

// SQL constants for migrations versions 26+. See migrate.go for the
// Migrate driver and the registry slice. Each const corresponds to a
// `{version: N, name: ..., sql: <name>}` entry in `migrations`; the
// last entry in the slice typically lives here.

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
const migrationAddPhase2Features = `
-- Phase 2: Milestones, Estimates, Sub-issues, Workflow States, Notifications,
-- Saved Views, Recurring Issues, Triage Rules, Cost Budgets

-- 1. Milestones within projects
CREATE TABLE IF NOT EXISTS milestones (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT,
    target_date TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','completed','cancelled')),
    position INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_milestones_project ON milestones(project_id);

-- Link issues to milestones
ALTER TABLE missions ADD COLUMN milestone_id TEXT REFERENCES milestones(id);
CREATE INDEX IF NOT EXISTS idx_mission_milestone ON missions(milestone_id);

-- 2. Estimates (story points)
ALTER TABLE missions ADD COLUMN estimate INTEGER;

-- 3. Sub-issues (parent-child hierarchy)
ALTER TABLE missions ADD COLUMN parent_issue_id TEXT REFERENCES missions(id);
CREATE INDEX IF NOT EXISTS idx_mission_parent ON missions(parent_issue_id);

-- 4. Cost budgets on projects
ALTER TABLE projects ADD COLUMN budget_tokens INTEGER;
ALTER TABLE projects ADD COLUMN budget_cost REAL;

-- 5. Custom workflow states per crew
CREATE TABLE IF NOT EXISTS workflow_states (
    id TEXT PRIMARY KEY,
    crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    category TEXT NOT NULL CHECK(category IN ('backlog','unstarted','started','completed','cancelled')),
    color TEXT NOT NULL DEFAULT '#6B7280',
    position INTEGER NOT NULL DEFAULT 0,
    is_default INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(crew_id, name)
);
CREATE INDEX IF NOT EXISTS idx_workflow_states_crew ON workflow_states(crew_id);

-- 6. Persistent notifications
CREATE TABLE IF NOT EXISTS notifications (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK(actor_type IN ('user','agent','system')),
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT,
    entity_title TEXT,
    read_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, read_at);
CREATE INDEX IF NOT EXISTS idx_notifications_ws ON notifications(workspace_id);

-- 7. Saved views (filter presets)
CREATE TABLE IF NOT EXISTS saved_views (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    filters_json TEXT NOT NULL DEFAULT '{}',
    sort_json TEXT,
    view_type TEXT NOT NULL DEFAULT 'board' CHECK(view_type IN ('board','list')),
    is_default INTEGER NOT NULL DEFAULT 0,
    shared INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_saved_views_user ON saved_views(user_id, workspace_id);

-- 8. Recurring issues (cron-based)
CREATE TABLE IF NOT EXISTS recurring_issues (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    crew_id TEXT NOT NULL REFERENCES crews(id),
    title TEXT NOT NULL,
    description TEXT,
    priority TEXT NOT NULL DEFAULT 'none',
    project_id TEXT REFERENCES projects(id),
    milestone_id TEXT REFERENCES milestones(id),
    assignee_type TEXT,
    assignee_id TEXT,
    labels_json TEXT DEFAULT '[]',
    cron_expression TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    next_run TEXT,
    last_run TEXT,
    run_count INTEGER NOT NULL DEFAULT 0,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_recurring_issues_next ON recurring_issues(next_run, enabled);

-- 9. AI triage rules
CREATE TABLE IF NOT EXISTS triage_rules (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL,
    pattern TEXT NOT NULL,
    match_type TEXT NOT NULL DEFAULT 'contains' CHECK(match_type IN ('contains','regex','exact')),
    crew_id TEXT REFERENCES crews(id),
    assignee_id TEXT,
    priority TEXT,
    project_id TEXT REFERENCES projects(id),
    labels_json TEXT DEFAULT '[]',
    position INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    match_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_triage_rules_ws ON triage_rules(workspace_id, enabled);
`

// v43: AddFKIndexes
const migrationAddFKIndexes = `
-- Foreign-key and hot-column indexes missed by earlier migrations.
-- All additive; safe to re-run via IF NOT EXISTS.

-- NextAuth FKs: scanned on every session/account lookup
CREATE INDEX IF NOT EXISTS idx_accounts_user ON accounts(userId);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(userId);

-- Workspace invitations: who invited whom
CREATE INDEX IF NOT EXISTS idx_invitation_invited_by ON workspace_invitations(invited_by);

-- Chats: created_by FK (existing idx_chat_agent / idx_chat_workspace don't cover this)
CREATE INDEX IF NOT EXISTS idx_chat_created_by ON chats(created_by);

-- Assignments: pagination query in assignments.go joins via workspace_id
CREATE INDEX IF NOT EXISTS idx_assignment_workspace ON assignments(workspace_id);

-- Skill authorship & reviews
CREATE INDEX IF NOT EXISTS idx_skill_author ON skills(author_id);
CREATE INDEX IF NOT EXISTS idx_skill_review_user ON skill_reviews(user_id);

-- agent_skills: UNIQUE(agent_id, skill_id) covers agent_id lookups but NOT skill_id
CREATE INDEX IF NOT EXISTS idx_agent_skill_skill ON agent_skills(skill_id);

-- agent_credentials: UNIQUE + idx_agent_credential_env lead with agent_id; credential_id is uncovered
CREATE INDEX IF NOT EXISTS idx_agent_credential_cred ON agent_credentials(credential_id);

-- credentials.created_by: audit / ownership lookups
CREATE INDEX IF NOT EXISTS idx_credential_created_by ON credentials(created_by);

-- subscriptions: no secondary indexes at all
CREATE INDEX IF NOT EXISTS idx_subscription_plan ON subscriptions(plan_id);

-- feature_flag_overrides: UNIQUE(flag_id, workspace_id) covers flag_id but NOT workspace_id
CREATE INDEX IF NOT EXISTS idx_feature_flag_override_ws ON feature_flag_overrides(workspace_id);

-- agent_config_history.changed_by
CREATE INDEX IF NOT EXISTS idx_config_history_changed_by ON agent_config_history(changed_by);

-- agent_runs: chat_id and triggered_by FKs
CREATE INDEX IF NOT EXISTS idx_run_chat ON agent_runs(chat_id);
CREATE INDEX IF NOT EXISTS idx_run_triggered_by ON agent_runs(triggered_by);

-- mission_tasks.assignment_id
CREATE INDEX IF NOT EXISTS idx_mission_task_assignment ON mission_tasks(assignment_id);

-- mission_proposals.proposed_by_id
CREATE INDEX IF NOT EXISTS idx_mission_proposal_proposer ON mission_proposals(proposed_by_id);
`

// v44: AddPerformanceIndexes
const migrationAddPerformanceIndexes = `
-- Index for issue list filtering by assignee
CREATE INDEX IF NOT EXISTS idx_mission_assignee ON missions(assignee_id) WHERE assignee_id IS NOT NULL;

-- Compound index for the most common issue list filter pattern
CREATE INDEX IF NOT EXISTS idx_mission_ws_type_status ON missions(workspace_id, mission_type, status);

-- Index for sub-issue counting (parent_issue_id already indexed by idx_mission_parent, but compound helps)
CREATE INDEX IF NOT EXISTS idx_mission_parent_ws ON missions(parent_issue_id, workspace_id) WHERE parent_issue_id IS NOT NULL;

-- Index for notification entity lookups
CREATE INDEX IF NOT EXISTS idx_notifications_entity ON notifications(entity_type, entity_id);

-- Index for recurring issues workspace filtering
CREATE INDEX IF NOT EXISTS idx_recurring_issues_ws ON recurring_issues(workspace_id);
`

// v52: AddCrewJournal
const migrationAddCrewJournal = `
-- Crew Journal: append-only event stream. Canonical source of truth for every
-- observable action in the platform. Entries are immutable once written; any
-- correction is a new entry that references the original via refs.parent_entry_id.
CREATE TABLE IF NOT EXISTS journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info' CHECK(severity IN ('info','notice','warn','error')),
    actor_type TEXT NOT NULL CHECK(actor_type IN ('agent','user','system','keeper','sidecar','orchestrator')),
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_journal_ws_ts ON journal_entries(workspace_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_crew_ts ON journal_entries(crew_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_agent_ts ON journal_entries(agent_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_mission_ts ON journal_entries(mission_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_type_ts ON journal_entries(entry_type, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_severity ON journal_entries(severity, ts DESC) WHERE severity IN ('warn','error');
CREATE INDEX IF NOT EXISTS idx_journal_expires ON journal_entries(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_journal_trace ON journal_entries(trace_id) WHERE trace_id IS NOT NULL;

-- Selective embedding index for episodic recall. Embedding stored as a raw
-- float32[] BLOB; dim is recorded per row so the recall code can sanity-check
-- on load. Only high-value entries get embedded (peer.escalation, summaries,
-- terminal status changes, denied keeper decisions, operator-tagged entries)
-- to prevent the memory-drift anti-pattern where every tool-chunk dilutes
-- recall quality.
CREATE TABLE IF NOT EXISTS journal_embeddings (
    entry_id TEXT PRIMARY KEY REFERENCES journal_entries(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    dim INTEGER NOT NULL,
    vector BLOB NOT NULL,
    indexed_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_journal_emb_agent ON journal_embeddings(agent_id, indexed_at DESC);
CREATE INDEX IF NOT EXISTS idx_journal_emb_crew ON journal_embeddings(crew_id, indexed_at DESC);
CREATE INDEX IF NOT EXISTS idx_journal_emb_ws ON journal_embeddings(workspace_id, indexed_at DESC);

-- Watch Roster: agent presence. Auto-lifecycle from orchestrator (online on
-- run accept, busy during run, blocked on keeper/escalation wait, offline
-- after idle > 5 min). Emits agent.status_change entries into the journal;
-- this table is the last-write-wins snapshot for fast UI queries.
CREATE TABLE IF NOT EXISTS agent_status (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'offline' CHECK(status IN ('online','busy','blocked','offline')),
    since TEXT NOT NULL DEFAULT (datetime('now')),
    details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_agent_status_crew ON agent_status(crew_id, status);
CREATE INDEX IF NOT EXISTS idx_agent_status_ws ON agent_status(workspace_id, status);

-- Harbor Master: HITL approval queue. Separate from mission_tasks.approval_*
-- columns which are task-scoped; this queue covers any action requiring
-- human sign-off (tool call, cost threshold, target environment). Timed out
-- requests flip to 'denied' via the background sweeper so agents can fail
-- deterministically instead of blocking forever.
CREATE TABLE IF NOT EXISTS approvals_queue (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    requested_by TEXT NOT NULL,
    kind TEXT NOT NULL,
    reason TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','denied','timeout','cancelled')),
    decided_by TEXT,
    decided_at TEXT,
    decision_comment TEXT,
    timeout_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals_queue(status, timeout_at);
CREATE INDEX IF NOT EXISTS idx_approvals_ws ON approvals_queue(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approvals_agent ON approvals_queue(agent_id, status);

-- Cartographer: mission checkpoints. journal_cursor is the last entry ID
-- included in the snapshot; restore replays the journal up to that cursor
-- and applies state_snapshot to rebuild in-memory state (agent memory,
-- pending tasks, open assignments). fork_of points to the parent checkpoint
-- when a mission was branched.
CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    mission_id TEXT REFERENCES missions(id) ON DELETE CASCADE,
    label TEXT,
    journal_cursor TEXT NOT NULL,
    state_snapshot TEXT NOT NULL DEFAULT '{}',
    fork_of TEXT REFERENCES checkpoints(id) ON DELETE SET NULL,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_mission ON checkpoints(mission_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_checkpoints_ws ON checkpoints(workspace_id, created_at DESC);

-- Hooks: lifecycle intercept registrations. matcher is a JSON predicate
-- (event fields + regex); handler encodes the dispatch kind (shell, http,
-- subagent) with type-specific config. Shell hooks require OWNER role at
-- registration time (enforced in the handler, not at schema level).
CREATE TABLE IF NOT EXISTS hooks_config (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    matcher TEXT NOT NULL DEFAULT '{}',
    handler_kind TEXT NOT NULL CHECK(handler_kind IN ('shell','http','subagent')),
    handler_config TEXT NOT NULL DEFAULT '{}',
    blocking INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_hooks_event ON hooks_config(event, enabled);
CREATE INDEX IF NOT EXISTS idx_hooks_ws ON hooks_config(workspace_id, enabled);

-- Paymaster: per-call cost ledger. One row per LLM invocation. Denormalized
-- tokens + cost so the rollup view can aggregate without joins. Cached input
-- tokens are tracked separately so cache hit rate is computable per agent.
CREATE TABLE IF NOT EXISTS cost_ledger (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    tags TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_cost_ws_ts ON cost_ledger(workspace_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_cost_crew_ts ON cost_ledger(crew_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_cost_agent_ts ON cost_ledger(agent_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_cost_mission ON cost_ledger(mission_id, ts DESC);

-- Hierarchical budget limits. A budget row at any level (workspace, crew,
-- mission, agent) caps cumulative cost_ledger spend in the given window.
-- Enforcement is soft (warn) below 80% of limit, hard (deny next call)
-- at 100%. The middleware consults all applicable budgets in ascending
-- scope specificity.
CREATE TABLE IF NOT EXISTS budget_limits (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    scope_kind TEXT NOT NULL CHECK(scope_kind IN ('workspace','crew','mission','agent')),
    scope_id TEXT NOT NULL,
    window TEXT NOT NULL CHECK(window IN ('hour','day','week','month','mission')),
    limit_usd REAL NOT NULL,
    mode TEXT NOT NULL DEFAULT 'tiered' CHECK(mode IN ('soft','hard','tiered')),
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, scope_kind, scope_id, window)
);
CREATE INDEX IF NOT EXISTS idx_budget_scope ON budget_limits(scope_kind, scope_id, enabled);
`

