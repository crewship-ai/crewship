package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

func Migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	for _, m := range migrations {
		var exists bool
		err := db.QueryRowContext(ctx, "SELECT 1 FROM _migrations WHERE version = ?", m.version).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check migration %d (%s): %w", m.version, m.name, err)
		}

		logger.Info("applying migration", "version", m.version, "name", m.name)

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d (%s): %w", m.version, m.name, err)
		}

		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO _migrations (version, name) VALUES (?, ?)", m.version, m.name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d (%s): %w", m.version, m.name, err)
		}
	}

	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{1, "init", migrationInit},
	{2, "add_onboarding_completed", migrationAddOnboardingCompleted},
	{3, "add_memory_config", migrationAddMemoryConfig},
	{4, "add_lead_mode", migrationAddLeadMode},
	{5, "add_preferred_language", migrationAddPreferredLanguage},
	{6, "add_peer_conversations", migrationAddPeerConversations},
	{7, "add_escalations", migrationAddEscalations},
	{8, "add_missions", migrationAddMissions},
	{9, "add_keeper", migrationAddKeeper},
	{10, "add_keeper_execute", migrationAddKeeperExecute},
	{11, "add_keeper_observability", migrationAddKeeperObservability},
	{12, "add_cli_tokens", migrationAddCLITokens},
	{13, "add_chat_agent_status_index", migrationAddChatAgentStatusIndex},
	{14, "add_agent_avatar_seed", migrationAddAgentAvatarSeed},
	{15, "add_avatar_style", migrationAddAvatarStyle},
	{16, "add_agent_cli_tools", migrationAddAgentCLITools},
	{17, "add_credential_crews", migrationAddCredentialCrews},
	{18, "add_crew_network_policy", migrationAddCrewNetworkPolicy},
	{19, "add_workflow_templates", migrationAddWorkflowTemplates},
	{20, "add_crew_connections", migrationAddCrewConnections},
	{21, "add_mission_proposals", migrationAddMissionProposals},
	{22, "add_escalation_type_and_resolve", migrationAddEscalationTypeAndResolve},
	{23, "add_crew_templates", migrationAddCrewTemplates},
}

const migrationAddKeeperObservability = `
-- Store the Ollama prompt and raw LLM response for keeper decision observability.
ALTER TABLE keeper_requests ADD COLUMN ollama_prompt TEXT;
ALTER TABLE keeper_requests ADD COLUMN ollama_raw_response TEXT;
`

const migrationAddCLITokens = `
CREATE TABLE IF NOT EXISTS cli_tokens (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	last_used_at TEXT,
	revoked_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_cli_token_user ON cli_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_cli_token_hash ON cli_tokens(token_hash);
`

const migrationAddOnboardingCompleted = `
ALTER TABLE users ADD COLUMN onboarding_completed INTEGER NOT NULL DEFAULT 0;
`

const migrationAddMemoryConfig = `
ALTER TABLE agents ADD COLUMN memory_config TEXT;
`

const migrationAddLeadMode = `
ALTER TABLE agents ADD COLUMN lead_mode TEXT DEFAULT 'active';
`

const migrationAddPreferredLanguage = `
ALTER TABLE workspaces ADD COLUMN preferred_language TEXT;
`

const migrationAddPeerConversations = `
CREATE TABLE IF NOT EXISTS peer_conversations (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	crew_id TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	from_agent_id TEXT NOT NULL,
	to_agent_id TEXT NOT NULL,
	question TEXT NOT NULL,
	response TEXT,
	status TEXT NOT NULL DEFAULT 'PENDING',
	duration_ms INTEGER,
	escalated INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	finished_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_peer_conv_crew ON peer_conversations(crew_id);
CREATE INDEX IF NOT EXISTS idx_peer_conv_from ON peer_conversations(from_agent_id);
CREATE INDEX IF NOT EXISTS idx_peer_conv_to ON peer_conversations(to_agent_id);
CREATE INDEX IF NOT EXISTS idx_peer_conv_created ON peer_conversations(created_at);
`

const migrationAddEscalations = `
CREATE TABLE IF NOT EXISTS escalations (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	crew_id TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	from_agent_id TEXT NOT NULL,
	peer_conversation_id TEXT,
	reason TEXT NOT NULL,
	context TEXT,
	status TEXT NOT NULL DEFAULT 'PENDING',
	resolution TEXT,
	resolved_at TEXT,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_escalation_crew ON escalations(crew_id);
CREATE INDEX IF NOT EXISTS idx_escalation_from ON escalations(from_agent_id);
CREATE INDEX IF NOT EXISTS idx_escalation_status ON escalations(status);
CREATE INDEX IF NOT EXISTS idx_escalation_created ON escalations(created_at);
`

const migrationAddMissions = `
CREATE TABLE IF NOT EXISTS missions (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	crew_id TEXT NOT NULL REFERENCES crews(id),
	lead_agent_id TEXT NOT NULL REFERENCES agents(id),
	trace_id TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL,
	description TEXT,
	status TEXT NOT NULL DEFAULT 'PLANNING',
	plan TEXT,
	workflow_template TEXT,
	total_token_count INTEGER,
	total_estimated_cost REAL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_mission_workspace ON missions(workspace_id);
CREATE INDEX IF NOT EXISTS idx_mission_crew ON missions(crew_id);
CREATE INDEX IF NOT EXISTS idx_mission_lead ON missions(lead_agent_id);
CREATE INDEX IF NOT EXISTS idx_mission_status ON missions(status);
CREATE INDEX IF NOT EXISTS idx_mission_created ON missions(created_at);

CREATE TABLE IF NOT EXISTS mission_tasks (
	id TEXT PRIMARY KEY,
	mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	assigned_agent_id TEXT REFERENCES agents(id),
	title TEXT NOT NULL,
	description TEXT,
	status TEXT NOT NULL DEFAULT 'PENDING',
	task_order INTEGER NOT NULL DEFAULT 0,
	depends_on TEXT DEFAULT '[]',
	iteration INTEGER DEFAULT 1,
	max_iterations INTEGER,
	result_summary TEXT,
	output_path TEXT,
	error_message TEXT,
	assignment_id TEXT REFERENCES assignments(id),
	token_count INTEGER,
	estimated_cost REAL,
	started_at TEXT,
	completed_at TEXT,
	duration_ms INTEGER,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_mission_task_mission ON mission_tasks(mission_id);
CREATE INDEX IF NOT EXISTS idx_mission_task_agent ON mission_tasks(assigned_agent_id);
CREATE INDEX IF NOT EXISTS idx_mission_task_status ON mission_tasks(status);
`

const migrationAddKeeper = `
-- Keeper credential security levels (L1-L4) and keeper crew assignment
ALTER TABLE credentials ADD COLUMN security_level INTEGER NOT NULL DEFAULT 1;
ALTER TABLE credentials ADD COLUMN keeper_crew_id TEXT;

-- Keeper request audit log
CREATE TABLE IF NOT EXISTS keeper_requests (
	id TEXT PRIMARY KEY,
	requesting_agent_id TEXT NOT NULL REFERENCES agents(id),
	requesting_crew_id TEXT NOT NULL,
	credential_id TEXT NOT NULL REFERENCES credentials(id),
	task_id TEXT,
	intent TEXT NOT NULL,
	decision TEXT,
	reason TEXT,
	risk_score INTEGER,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	decided_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_keeper_req_agent ON keeper_requests(requesting_agent_id);
CREATE INDEX IF NOT EXISTS idx_keeper_req_crew ON keeper_requests(requesting_crew_id);
CREATE INDEX IF NOT EXISTS idx_keeper_req_cred ON keeper_requests(credential_id);
CREATE INDEX IF NOT EXISTS idx_keeper_req_decision ON keeper_requests(decision);
CREATE INDEX IF NOT EXISTS idx_keeper_req_created ON keeper_requests(created_at);
`

const migrationAddKeeperExecute = `
-- Add execute request tracking to the keeper audit log.
-- request_type: 'access' (credential lookup) or 'execute' (run command with credential)
-- command: the shell command executed on behalf of the agent (execute requests only)
-- exit_code: the exit code of the executed command (execute requests only)
ALTER TABLE keeper_requests ADD COLUMN request_type TEXT NOT NULL DEFAULT 'access';
ALTER TABLE keeper_requests ADD COLUMN command TEXT;
ALTER TABLE keeper_requests ADD COLUMN exit_code INTEGER;
`

const migrationInit = `
-- Users (NextAuth.js compatible)
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	full_name TEXT,
	avatar_url TEXT,
	hashed_password TEXT,
	email_verified TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- NextAuth accounts
CREATE TABLE IF NOT EXISTS accounts (
	id TEXT PRIMARY KEY,
	userId TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	type TEXT NOT NULL,
	provider TEXT NOT NULL,
	providerAccountId TEXT NOT NULL,
	refresh_token TEXT,
	access_token TEXT,
	expires_at INTEGER,
	token_type TEXT,
	scope TEXT,
	id_token TEXT,
	session_state TEXT,
	UNIQUE(provider, providerAccountId)
);

-- NextAuth sessions
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	sessionToken TEXT NOT NULL UNIQUE,
	userId TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires TEXT NOT NULL
);

-- NextAuth verification tokens
CREATE TABLE IF NOT EXISTS verification_tokens (
	identifier TEXT NOT NULL,
	token TEXT NOT NULL UNIQUE,
	expires TEXT NOT NULL,
	UNIQUE(identifier, token)
);

-- Workspaces
CREATE TABLE IF NOT EXISTS workspaces (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	logo_url TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at TEXT,
	default_container_ttl_hours INTEGER
);

-- Workspace members
CREATE TABLE IF NOT EXISTS workspace_members (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role TEXT NOT NULL DEFAULT 'MEMBER',
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_member_workspace ON workspace_members(workspace_id);
CREATE INDEX IF NOT EXISTS idx_workspace_member_user ON workspace_members(user_id);

-- Workspace invitations
CREATE TABLE IF NOT EXISTS workspace_invitations (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'MEMBER',
	invited_by TEXT NOT NULL REFERENCES users(id),
	token TEXT NOT NULL UNIQUE,
	expires_at TEXT NOT NULL,
	accepted_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_invitation_workspace ON workspace_invitations(workspace_id);
CREATE INDEX IF NOT EXISTS idx_invitation_token ON workspace_invitations(token);
CREATE INDEX IF NOT EXISTS idx_invitation_email_workspace ON workspace_invitations(email, workspace_id);

-- Crews
CREATE TABLE IF NOT EXISTS crews (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	slug TEXT NOT NULL,
	description TEXT,
	color TEXT,
	icon TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at TEXT,
	container_ttl_hours INTEGER,
	container_memory_mb INTEGER NOT NULL DEFAULT 4096,
	container_cpus REAL NOT NULL DEFAULT 2.0,
	UNIQUE(workspace_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_crew_workspace ON crews(workspace_id);

-- Crew members
CREATE TABLE IF NOT EXISTS crew_members (
	id TEXT PRIMARY KEY,
	crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(crew_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_crew_member_crew ON crew_members(crew_id);
CREATE INDEX IF NOT EXISTS idx_crew_member_user ON crew_members(user_id);

-- Agents
CREATE TABLE IF NOT EXISTS agents (
	id TEXT PRIMARY KEY,
	crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	slug TEXT NOT NULL,
	description TEXT,
	role_title TEXT,
	agent_role TEXT NOT NULL DEFAULT 'AGENT',
	status TEXT NOT NULL DEFAULT 'IDLE',
	cli_adapter TEXT NOT NULL DEFAULT 'CLAUDE_CODE',
	llm_provider TEXT,
	llm_model TEXT,
	system_prompt TEXT,
	temperature REAL NOT NULL DEFAULT 0.7,
	max_tokens INTEGER,
	timeout_seconds INTEGER NOT NULL DEFAULT 1800,
	tool_profile TEXT NOT NULL DEFAULT 'CODING',
	memory_enabled INTEGER NOT NULL DEFAULT 0,
	webhook_secret TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at TEXT,
	delegation_timeout_s INTEGER,
	max_delegation_depth INTEGER DEFAULT 3,
	max_parallel_delegates INTEGER DEFAULT 5,
	UNIQUE(workspace_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_agent_workspace ON agents(workspace_id);
CREATE INDEX IF NOT EXISTS idx_agent_crew ON agents(crew_id);
CREATE INDEX IF NOT EXISTS idx_agent_status ON agents(status);
CREATE INDEX IF NOT EXISTS idx_agent_role ON agents(agent_role);

-- Chats (metadata only)
CREATE TABLE IF NOT EXISTS chats (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	created_by TEXT REFERENCES users(id),
	title TEXT,
	mode TEXT NOT NULL DEFAULT 'CHAT',
	status TEXT NOT NULL DEFAULT 'ACTIVE',
	message_count INTEGER NOT NULL DEFAULT 0,
	jsonl_path TEXT,
	started_at TEXT NOT NULL DEFAULT (datetime('now')),
	ended_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_agent ON chats(agent_id);
CREATE INDEX IF NOT EXISTS idx_chat_workspace ON chats(workspace_id);
CREATE INDEX IF NOT EXISTS idx_chat_created ON chats(created_at);

-- Assignments
CREATE TABLE IF NOT EXISTS assignments (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	chat_id TEXT NOT NULL REFERENCES chats(id),
	assigned_by_id TEXT NOT NULL REFERENCES agents(id),
	assigned_to_id TEXT NOT NULL REFERENCES agents(id),
	task TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'PENDING',
	started_at TEXT,
	finished_at TEXT,
	result_summary TEXT,
	error_message TEXT,
	group_id TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_assignment_chat ON assignments(chat_id);
CREATE INDEX IF NOT EXISTS idx_assignment_by ON assignments(assigned_by_id);
CREATE INDEX IF NOT EXISTS idx_assignment_to ON assignments(assigned_to_id);
CREATE INDEX IF NOT EXISTS idx_assignment_group ON assignments(group_id);

-- Skills
CREATE TABLE IF NOT EXISTS skills (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	slug TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	description TEXT,
	version TEXT NOT NULL DEFAULT '1.0.0',
	author TEXT,
	license TEXT DEFAULT 'MIT',
	category TEXT NOT NULL DEFAULT 'CUSTOM',
	source TEXT NOT NULL DEFAULT 'CUSTOM',
	config_schema TEXT,
	tool_definitions TEXT,
	content TEXT,
	icon TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	mcp_server_command TEXT,
	mcp_server_image TEXT,
	mcp_transport TEXT DEFAULT 'stdio',
	credential_requirements TEXT,
	dependencies TEXT,
	tool_count INTEGER,
	defer_loading INTEGER NOT NULL DEFAULT 0,
	verification TEXT NOT NULL DEFAULT 'UNVERIFIED',
	security_score INTEGER,
	security_report TEXT,
	downloads INTEGER NOT NULL DEFAULT 0,
	rating_avg REAL,
	rating_count INTEGER NOT NULL DEFAULT 0,
	tags TEXT DEFAULT '[]',
	featured INTEGER NOT NULL DEFAULT 0,
	oci_image TEXT,
	oci_digest TEXT,
	sbom_url TEXT,
	allowed_domains TEXT DEFAULT '[]',
	pricing_tier TEXT NOT NULL DEFAULT 'FREE',
	price_monthly INTEGER,
	author_id TEXT REFERENCES users(id),
	revenue_share_pct INTEGER DEFAULT 70,
	changelog TEXT
);
CREATE INDEX IF NOT EXISTS idx_skill_category ON skills(category);
CREATE INDEX IF NOT EXISTS idx_skill_source ON skills(source);
CREATE INDEX IF NOT EXISTS idx_skill_verification ON skills(verification);
CREATE INDEX IF NOT EXISTS idx_skill_featured ON skills(featured);

-- Skill reviews
CREATE TABLE IF NOT EXISTS skill_reviews (
	id TEXT PRIMARY KEY,
	skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id),
	rating INTEGER NOT NULL,
	title TEXT,
	body TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(skill_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_skill_review_skill ON skill_reviews(skill_id);

-- Agent skills (M:N)
CREATE TABLE IF NOT EXISTS agent_skills (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
	config TEXT,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(agent_id, skill_id)
);

-- Credentials
CREATE TABLE IF NOT EXISTS credentials (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	crew_id TEXT REFERENCES crews(id) ON DELETE SET NULL,
	name TEXT NOT NULL,
	description TEXT,
	encrypted_value TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT 'WORKSPACE',
	type TEXT NOT NULL DEFAULT 'SECRET',
	provider TEXT NOT NULL DEFAULT 'NONE',
	status TEXT NOT NULL DEFAULT 'ACTIVE',
	encrypted_refresh_token TEXT,
	token_expires_at TEXT,
	account_label TEXT,
	account_email TEXT,
	last_checked_at TEXT,
	last_error TEXT,
	created_by TEXT NOT NULL REFERENCES users(id),
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	deleted_at TEXT,
	UNIQUE(workspace_id, name)
);
CREATE INDEX IF NOT EXISTS idx_credential_workspace ON credentials(workspace_id);
CREATE INDEX IF NOT EXISTS idx_credential_crew ON credentials(crew_id);
CREATE INDEX IF NOT EXISTS idx_credential_type_provider ON credentials(workspace_id, type, provider);

-- Agent credentials (M:N, credential pool)
CREATE TABLE IF NOT EXISTS agent_credentials (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
	env_var_name TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(agent_id, credential_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_credential_env ON agent_credentials(agent_id, env_var_name);

-- Agent runs
CREATE TABLE IF NOT EXISTS agent_runs (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	chat_id TEXT REFERENCES chats(id),
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	triggered_by TEXT REFERENCES users(id),
	trigger_type TEXT NOT NULL DEFAULT 'USER',
	status TEXT NOT NULL DEFAULT 'PENDING',
	started_at TEXT,
	finished_at TEXT,
	error_message TEXT,
	exit_code INTEGER,
	metadata TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_run_agent_time ON agent_runs(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_run_workspace ON agent_runs(workspace_id);
CREATE INDEX IF NOT EXISTS idx_run_status ON agent_runs(status);

-- Audit logs
CREATE TABLE IF NOT EXISTS audit_logs (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	user_id TEXT REFERENCES users(id),
	action TEXT NOT NULL,
	entity_type TEXT NOT NULL,
	entity_id TEXT,
	metadata TEXT,
	ip_address TEXT,
	user_agent TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_audit_workspace_time ON audit_logs(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_logs(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);

-- Subscriptions
CREATE TABLE IF NOT EXISTS subscriptions (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
	plan_id TEXT NOT NULL REFERENCES plans(id),
	stripe_customer_id TEXT UNIQUE,
	stripe_subscription_id TEXT UNIQUE,
	status TEXT NOT NULL DEFAULT 'ACTIVE',
	current_period_start TEXT,
	current_period_end TEXT,
	cancel_at TEXT,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Plans
CREATE TABLE IF NOT EXISTS plans (
	id TEXT PRIMARY KEY,
	tier TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	stripe_price_id TEXT UNIQUE,
	max_agents INTEGER NOT NULL,
	max_crews INTEGER NOT NULL,
	max_skills INTEGER NOT NULL,
	max_credentials INTEGER NOT NULL,
	max_members INTEGER NOT NULL,
	features TEXT,
	price_monthly INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Feature flags
CREATE TABLE IF NOT EXISTS feature_flags (
	id TEXT PRIMARY KEY,
	key TEXT NOT NULL UNIQUE,
	description TEXT,
	enabled INTEGER NOT NULL DEFAULT 0,
	percentage INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Feature flag overrides
CREATE TABLE IF NOT EXISTS feature_flag_overrides (
	id TEXT PRIMARY KEY,
	flag_id TEXT NOT NULL REFERENCES feature_flags(id) ON DELETE CASCADE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	enabled INTEGER NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(flag_id, workspace_id)
);

-- Agent config history
CREATE TABLE IF NOT EXISTS agent_config_history (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	changed_by TEXT NOT NULL REFERENCES users(id),
	version INTEGER NOT NULL,
	changes TEXT NOT NULL,
	snapshot TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(agent_id, version)
);
CREATE INDEX IF NOT EXISTS idx_config_history_agent_time ON agent_config_history(agent_id, created_at);
`

const migrationAddChatAgentStatusIndex = `
CREATE INDEX IF NOT EXISTS idx_chat_agent_status_created ON chats(agent_id, status, created_at DESC);
`

const migrationAddAgentAvatarSeed = `
ALTER TABLE agents ADD COLUMN avatar_seed TEXT;
`

const migrationAddAvatarStyle = `
ALTER TABLE agents ADD COLUMN avatar_style TEXT;
ALTER TABLE crews ADD COLUMN avatar_style TEXT;
`

const migrationAddAgentCLITools = `
ALTER TABLE agents ADD COLUMN cli_tools TEXT;
`

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

const migrationAddCrewNetworkPolicy = `
ALTER TABLE crews ADD COLUMN network_mode TEXT NOT NULL DEFAULT 'free' CHECK(network_mode IN ('free', 'restricted'));
ALTER TABLE crews ADD COLUMN allowed_domains TEXT;
`

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

const migrationAddEscalationTypeAndResolve = `
-- Add type column to distinguish escalation kinds (TEXT, CREDENTIAL, LINK).
-- Default to TEXT for backwards compatibility with existing escalations.
ALTER TABLE escalations ADD COLUMN type TEXT NOT NULL DEFAULT 'TEXT' CHECK(type IN ('TEXT', 'CREDENTIAL', 'LINK'));

-- Add metadata column for structured data (e.g. link URL, credential env var name).
ALTER TABLE escalations ADD COLUMN metadata TEXT;

-- Add resolved_by to track who resolved the escalation (user/workspace member).
ALTER TABLE escalations ADD COLUMN resolved_by TEXT;
`

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
