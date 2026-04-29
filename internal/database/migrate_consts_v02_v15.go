package database

// SQL constants for migrations v2–v15: small additive changes to the
// baseline schema (onboarding flag, memory config, lead mode, language
// preference, peer conversations, escalations, missions, keeper family,
// CLI tokens, chat indexes, agent avatar attributes).

// v2: AddOnboardingCompleted
const migrationAddOnboardingCompleted = `
ALTER TABLE users ADD COLUMN onboarding_completed INTEGER NOT NULL DEFAULT 0;
`

// v3: AddMemoryConfig
const migrationAddMemoryConfig = `
ALTER TABLE agents ADD COLUMN memory_config TEXT;
`

// v4: AddLeadMode
const migrationAddLeadMode = `
ALTER TABLE agents ADD COLUMN lead_mode TEXT DEFAULT 'active';
`

// v5: AddPreferredLanguage
const migrationAddPreferredLanguage = `
ALTER TABLE workspaces ADD COLUMN preferred_language TEXT;
`

// v6: AddPeerConversations
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

// v7: AddEscalations
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

// v8: AddMissions
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

// v9: AddKeeper
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

// v10: AddKeeperExecute
const migrationAddKeeperExecute = `
-- Add execute request tracking to the keeper audit log.
-- request_type: 'access' (credential lookup) or 'execute' (run command with credential)
-- command: the shell command executed on behalf of the agent (execute requests only)
-- exit_code: the exit code of the executed command (execute requests only)
ALTER TABLE keeper_requests ADD COLUMN request_type TEXT NOT NULL DEFAULT 'access';
ALTER TABLE keeper_requests ADD COLUMN command TEXT;
ALTER TABLE keeper_requests ADD COLUMN exit_code INTEGER;
`

// v11: AddKeeperObservability
const migrationAddKeeperObservability = `
-- Store the Ollama prompt and raw LLM response for keeper decision observability.
ALTER TABLE keeper_requests ADD COLUMN ollama_prompt TEXT;
ALTER TABLE keeper_requests ADD COLUMN ollama_raw_response TEXT;
`

// v12: AddCLITokens
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

// v13: AddChatAgentStatusIndex
const migrationAddChatAgentStatusIndex = `
CREATE INDEX IF NOT EXISTS idx_chat_agent_status_created ON chats(agent_id, status, created_at DESC);
`

// v14: AddAgentAvatarSeed
const migrationAddAgentAvatarSeed = `
ALTER TABLE agents ADD COLUMN avatar_seed TEXT;
`

// v15: AddAvatarStyle
const migrationAddAvatarStyle = `
ALTER TABLE agents ADD COLUMN avatar_style TEXT;
ALTER TABLE crews ADD COLUMN avatar_style TEXT;
`

