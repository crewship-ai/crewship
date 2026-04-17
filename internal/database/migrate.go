package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// Migrate applies all pending schema migrations to the database in order.
// Migrations are tracked in the _migrations table to ensure idempotency.
// This is the sole mechanism for schema changes; Prisma is not used for migrations.
func Migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	for _, m := range migrations {
		var appliedName string
		err := db.QueryRowContext(ctx, "SELECT name FROM _migrations WHERE version = ?", m.version).Scan(&appliedName)
		if err == nil {
			// Version already applied. Guard against the classic two-branch-merge
			// collision: both PRs claim the same version number with different SQL.
			// If names disagree, the DB has one migration applied while the code
			// expects another, and silently continuing would leave prod and dev
			// on diverged schemas.
			if appliedName != m.name {
				return fmt.Errorf(
					"migration version %d collision: database has %q applied, code expects %q — "+
						"rename the new migration to the next free version",
					m.version, appliedName, m.name,
				)
			}
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

		// Migrations are either a static SQL string or a Go function — not
		// both. SQL migrations cover the vast majority; fn migrations exist
		// for the rare case where we need to discover schema state at apply
		// time (e.g. iterate pragma_table_info to find legacy columns).
		if m.fn != nil {
			if err := m.fn(ctx, tx, logger); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
		} else {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
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
	// fn, if set, runs instead of sql. Receives the migration's transaction
	// so its work commits atomically with the _migrations row.
	fn func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error
	// restoreBackfill, if set, runs during RestoreBackup against the rows
	// just re-inserted from a bundle whose source schema predates this
	// migration. The bundle's manifest records the migration versions
	// applied on the source; when the target has additional migrations
	// applied (target > source) the backup subsystem calls the hook for
	// each such migration so newly-added columns get populated on the
	// restored rows. Pure ADD COLUMN migrations that rely on the DB
	// DEFAULT need no hook. See internal/backup/runner.go RestoreBackup.
	restoreBackfill RestoreBackfillFunc
}

// RestoreBackfillFunc is the signature for per-migration hooks that
// populate newly-added columns on rows just restored from an older
// bundle. Runs in its own transaction after the main restore tx has
// committed successfully; a returned error aborts the restore but does
// not roll back the already-committed row inserts — callers log
// loudly and prompt the operator to investigate.
type RestoreBackfillFunc func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error

var migrations = []migration{
	{version: 1, name: "init", sql: migrationInit},
	{version: 2, name: "add_onboarding_completed", sql: migrationAddOnboardingCompleted},
	{version: 3, name: "add_memory_config", sql: migrationAddMemoryConfig},
	{version: 4, name: "add_lead_mode", sql: migrationAddLeadMode},
	{version: 5, name: "add_preferred_language", sql: migrationAddPreferredLanguage},
	{version: 6, name: "add_peer_conversations", sql: migrationAddPeerConversations},
	{version: 7, name: "add_escalations", sql: migrationAddEscalations},
	{version: 8, name: "add_missions", sql: migrationAddMissions},
	{version: 9, name: "add_keeper", sql: migrationAddKeeper},
	{version: 10, name: "add_keeper_execute", sql: migrationAddKeeperExecute},
	{version: 11, name: "add_keeper_observability", sql: migrationAddKeeperObservability},
	{version: 12, name: "add_cli_tokens", sql: migrationAddCLITokens},
	{version: 13, name: "add_chat_agent_status_index", sql: migrationAddChatAgentStatusIndex},
	{version: 14, name: "add_agent_avatar_seed", sql: migrationAddAgentAvatarSeed},
	{version: 15, name: "add_avatar_style", sql: migrationAddAvatarStyle},
	{version: 16, name: "add_agent_cli_tools", sql: migrationAddAgentCLITools},
	{version: 17, name: "add_credential_crews", sql: migrationAddCredentialCrews},
	{version: 18, name: "add_crew_network_policy", sql: migrationAddCrewNetworkPolicy},
	{version: 19, name: "add_workflow_templates", sql: migrationAddWorkflowTemplates},
	{version: 20, name: "add_crew_connections", sql: migrationAddCrewConnections},
	// NOTE: mission_proposals was written by Captain (deprecated) and COORDINATOR
	// (deprecated) as of 2026-04-16. Table RETAINED for potential reuse in a
	// future human-in-the-loop approval flow. Do not drop without HITL decision.
	{version: 21, name: "add_mission_proposals", sql: migrationAddMissionProposals},
	{version: 22, name: "add_escalation_type_and_resolve", sql: migrationAddEscalationTypeAndResolve},
	{version: 23, name: "add_crew_templates", sql: migrationAddCrewTemplates},
	{version: 24, name: "add_agent_schedule", sql: migrationAddAgentSchedule},
	// Deprecated: backs the deprecated Captain feature (see internal/api/captain.go).
	// Do NOT drop — MVP policy "ship fast, gate later" keeps the table while the
	// Captain code remains for backward compat.
	{version: 25, name: "add_captain_chats", sql: migrationAddCaptainChats},
	{version: 26, name: "add_crew_templates_workspace_id", sql: migrationAddCrewTemplatesWorkspaceID},
	{version: 27, name: "add_escalation_action", sql: migrationAddEscalationAction},
	{version: 28, name: "add_task_scaling_and_handoff", sql: migrationAddTaskScalingAndHandoff},
	{version: 29, name: "add_mcp_gateway", sql: migrationAddMCPGateway},
	{version: 30, name: "fix_mcp_gateway_constraints", sql: migrationFixMCPGatewayConstraints},
	{version: 31, name: "add_mcp_binding_env_var", sql: migrationAddMCPBindingEnvVar},
	{version: 32, name: "add_oauth_credentials", sql: migrationAddOAuthCredentials},
	{version: 33, name: "add_mcp_config_json", sql: migrationAddMCPConfigJSON},
	{version: 34, name: "add_approval_gates", sql: migrationAddApprovalGates},
	{version: 35, name: "add_pkce_code_verifier", sql: migrationAddPKCECodeVerifier},
	{version: 36, name: "add_mcp_registry_cache", sql: migrationAddMCPRegistryCache},
	{version: 37, name: "add_crew_messaging_and_audit", sql: migrationAddCrewMessagingAndAudit},
	{version: 38, name: "add_issue_tracker", sql: migrationAddIssueTracker},
	{version: 39, name: "add_issue_relations", sql: migrationAddIssueRelations},
	{version: 40, name: "add_projects", sql: migrationAddProjects},
	{version: 41, name: "add_issue_activity", sql: migrationAddIssueActivity},
	{version: 42, name: "add_phase2_features", sql: migrationAddPhase2Features},
	// add_fk_indexes landed at version 43 via PR #132 on main; kept at 43
	// here since main already records that slot.
	{version: 43, name: "add_fk_indexes", sql: migrationAddFKIndexes},
	// add_performance_indexes and backfill_legacy_timestamps originated on
	// feat/performance at versions 43 and 44 respectively. They collided
	// with main's version 43 during the merge and were renumbered to 44/45
	// — the collision-check in Migrate would have failed loudly on startup
	// otherwise. Both renumberings are safe: the migrations are idempotent
	// (CREATE INDEX IF NOT EXISTS for the indexes; LIKE-gated UPDATEs for
	// the backfill) and don't depend on each other.
	{version: 44, name: "add_performance_indexes", sql: migrationAddPerformanceIndexes},
	{version: 45, name: "backfill_legacy_timestamps", fn: migrationBackfillLegacyTimestamps},
	{version: 46, name: "add_devcontainer_provisioning", sql: `
ALTER TABLE crews ADD COLUMN runtime_image TEXT;
ALTER TABLE crews ADD COLUMN devcontainer_config TEXT;
ALTER TABLE crews ADD COLUMN mise_config TEXT;
ALTER TABLE crews ADD COLUMN cached_image TEXT;
ALTER TABLE crews ADD COLUMN config_hash TEXT;
`},
	// Aggregated runtime requirements bubbled up from devcontainer features
	// (privileged, capAdd, mounts, containerEnv). Stored as JSON so the runtime
	// can apply them to HostConfig when starting a crew container. Without this,
	// features like DinD (which need privileged:true + /var/run/docker.sock)
	// would silently not work — the feature installs fine, but the final
	// container lacks the capabilities the feature requires.
	{version: 47, name: "add_cached_requirements", sql: `
ALTER TABLE crews ADD COLUMN cached_requirements TEXT;
`},
	// Per-workspace advisory lock used by the backup subsystem (CRE-126).
	// `backup create` acquires the row before pausing containers and
	// writing the tar.zst bundle; concurrent backups on the same
	// workspace are refused with ErrLockHeld. The TTL column lets a
	// crashed backup be reclaimed after one hour without operator
	// intervention. See .claude/context/prd/BACKUP.md section 4.3.
	{version: 48, name: "add_backup_locks", sql: `
CREATE TABLE IF NOT EXISTS backup_locks (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    acquired_at TEXT NOT NULL DEFAULT (datetime('now')),
    acquired_by TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_backup_locks_expires ON backup_locks(expires_at);
`},
	// Backup catalog — fast list of known bundles so the admin UI does
	// not have to filesystem-scan and parse every manifest on each
	// refresh. Populated on CreateBackup success, pruned on Delete. An
	// idempotent startup scan in internal/backup/catalog.go walks the
	// default backups dir and back-fills rows for bundles that existed
	// before this migration. See CRE-128 in .claude/context/prd/BACKUP.md.
	{version: 49, name: "add_backup_catalog", sql: `
CREATE TABLE IF NOT EXISTS backup_catalog (
    id TEXT PRIMARY KEY,
    file_path TEXT NOT NULL UNIQUE,
    scope TEXT NOT NULL,
    slug TEXT,
    workspace_id TEXT,
    created_at TEXT NOT NULL,
    created_by TEXT,
    size INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    encrypted INTEGER NOT NULL,
    format_version INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_backup_catalog_workspace ON backup_catalog(workspace_id);
CREATE INDEX IF NOT EXISTS idx_backup_catalog_created_at ON backup_catalog(created_at);
`},
	// Persistent instance identity for CRE-129 (instance-scope backup /
	// restore). Single-row table (CHECK id=1) so the row always exists
	// once migration runs; hostname is populated at first startup by
	// internal/backup/instance.go. When the manifest's source hostname
	// differs from the target's on restore, the flow forces an auth-key
	// rotation because we are clearly on a different host.
	{version: 50, name: "add_instance_config", sql: `
CREATE TABLE IF NOT EXISTS instance_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    hostname TEXT NOT NULL DEFAULT '',
    installed_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT OR IGNORE INTO instance_config (id, hostname) VALUES (1, '');
`},
	// Port exposures — agent-initiated reverse proxy registrations. A row
	// holds the opaque token (capability URL), the target container endpoint,
	// and the lifecycle state. MVP defaults every new row to ACTIVE via an
	// open-by-default policy; PENDING is reserved for a future approval layer
	// that can be added without breaking the schema. Indexed on token for
	// proxy lookups and on (status, expires_at) for the TTL purge goroutine.
	{version: 51, name: "add_port_exposures", sql: `
CREATE TABLE IF NOT EXISTS port_exposures (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
    token TEXT NOT NULL UNIQUE,
    container_id TEXT NOT NULL,
    container_ip TEXT NOT NULL,
    container_port INTEGER NOT NULL CHECK(container_port BETWEEN 1 AND 65535),
    description TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('PENDING','ACTIVE','REVOKED','EXPIRED')),
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    revoked_reason TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- token already has an implicit index from the UNIQUE constraint.
CREATE INDEX IF NOT EXISTS idx_port_exposures_workspace ON port_exposures(workspace_id, status);
CREATE INDEX IF NOT EXISTS idx_port_exposures_expires ON port_exposures(status, expires_at);
`},
	// Crew Journal foundation: a single append-only event stream (journal_entries)
	// becomes the canonical source of truth for everything that happens in the
	// platform — communication, missions, keeper decisions, exec/network/file
	// observability, cost, memory updates, checkpoints, hooks, approvals. The
	// older narrow tables (peer_conversations, escalations, mission_activity,
	// crew_audit_log) are backfilled in a subsequent migration and dropped once
	// verified. Ship's-log naming lives in the UI; schema uses "journal" for
	// clarity across Go code.
	//
	// Embeddings live in journal_embeddings as raw float32 BLOBs — SQLite has no
	// pgvector, so recall uses brute-force cosine over a selective subset
	// (escalations, summaries, terminal state changes, denied keeper decisions).
	// For Crewship's expected scale (~1-5% of entries embedded, thousands per
	// agent) a full scan completes in low milliseconds.
	//
	// Other tables added here are the foundation for Paymaster (budgets),
	// Harbor Master (approvals_queue), Cartographer (checkpoints), Hooks
	// (hooks_config), and Watch Roster (agent_status). They are created
	// together because they are interdependent and shipped as one feature set.
	{version: 52, name: "add_crew_journal", sql: migrationAddCrewJournal},
	// eval_runs: durable index of every quartermaster replay / regression
	// invocation. The journal already records each run as an
	// eval.run_started entry + per-metric eval.metric entries, but that
	// is a write-only trail optimised for audit, not list / filter /
	// paginate. This table gives the /api/v1/eval/runs endpoint a tight
	// SELECT over run rows plus status/result, so the UI (and CLI)
	// doesn't have to reconstruct a run by walking the journal.
	//
	// workspace_id is a hard scope predicate — cross-tenant leakage here
	// would expose full trajectory metrics from other customers, so the
	// handler always includes workspace_id in the WHERE clause.
	{version: 53, name: "add_eval_runs", sql: `
CREATE TABLE IF NOT EXISTS eval_runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('replay','regression')),
    mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    baseline_mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    candidate_mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','completed','failed')),
    result TEXT,
    seed INTEGER NOT NULL DEFAULT 0,
    signature TEXT,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    regressed INTEGER NOT NULL DEFAULT 0,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_eval_runs_ws_created ON eval_runs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_eval_runs_kind ON eval_runs(kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_eval_runs_mission ON eval_runs(mission_id, created_at DESC) WHERE mission_id IS NOT NULL;
`},
}

// restoreBackfillOverrides lets tests wire a hook without touching the
// main migrations slice. Keyed by version; a registered fn shadows
// whatever the migration's own restoreBackfill would return. Access
// goes through restoreBackfillMu because Go's test runner executes
// functions in parallel by default, and tests in
// restorer_backfill_test.go all register+unregister overrides.
var (
	restoreBackfillOverrides   = map[int]RestoreBackfillFunc{}
	restoreBackfillOverridesMu sync.RWMutex
)

// RestoreBackfillFor returns the hook registered for the given
// migration version, or nil if none. Consulted by the backup runner
// during RestoreBackup so each missing-on-source-but-applied-on-target
// migration can populate its added columns on the restored rows.
//
// The lookup prefers test overrides over the baked-in migration hook,
// so a test can exercise the replay plumbing without mutating the
// package's migration table.
func RestoreBackfillFor(version int) RestoreBackfillFunc {
	restoreBackfillOverridesMu.RLock()
	fn, ok := restoreBackfillOverrides[version]
	restoreBackfillOverridesMu.RUnlock()
	if ok {
		return fn
	}
	for _, m := range migrations {
		if m.version == version {
			return m.restoreBackfill
		}
	}
	return nil
}

// RegisterRestoreBackfill installs a hook for the given migration
// version, returning a deregister closure. Intended for tests. A
// second call for the same version replaces the prior registration.
func RegisterRestoreBackfill(version int, fn RestoreBackfillFunc) (unregister func()) {
	restoreBackfillOverridesMu.Lock()
	prev, had := restoreBackfillOverrides[version]
	restoreBackfillOverrides[version] = fn
	restoreBackfillOverridesMu.Unlock()
	return func() {
		restoreBackfillOverridesMu.Lock()
		defer restoreBackfillOverridesMu.Unlock()
		if had {
			restoreBackfillOverrides[version] = prev
		} else {
			delete(restoreBackfillOverrides, version)
		}
	}
}

// RollbackV47 reverts the schema change introduced by migration v47
// (add_cached_requirements). Intended as an operator escape hatch when a bad
// devcontainer rollout needs the runtime-requirements column removed in place.
//
// The Migrate framework is forward-only; this helper is called manually
// (planned future CLI: `crewship admin migrate rollback --version 47`).
// SQLite 3.35+ supports `ALTER TABLE DROP COLUMN` natively (modernc.org/sqlite
// tracks recent SQLite releases, so no CREATE-TABLE-AS-SELECT gymnastics are
// needed on current builds).
//
// The function is idempotent — dropping a missing column is treated as a
// no-op — and also removes the _migrations row so Migrate() will re-apply v47
// on next startup if the caller wants to replay it cleanly.
func RollbackV47(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollback tx: %w", err)
	}
	defer tx.Rollback()

	// Check if the column exists before attempting DROP — SQLite errors on
	// "no such column" even when wrapped in IF EXISTS-style patterns.
	var hasCol int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('crews') WHERE name = 'cached_requirements'`,
	).Scan(&hasCol); err != nil {
		return fmt.Errorf("probe cached_requirements column: %w", err)
	}
	if hasCol > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE crews DROP COLUMN cached_requirements`); err != nil {
			return fmt.Errorf("drop cached_requirements column: %w", err)
		}
		logger.Info("rollback v47: dropped crews.cached_requirements")
	} else {
		logger.Info("rollback v47: column already absent; skipping DROP")
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM _migrations WHERE version = 47`); err != nil {
		return fmt.Errorf("delete _migrations row for v47: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rollback: %w", err)
	}
	return nil
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

// migrationAddCaptainChats: Deprecated schema for the deprecated Captain feature.
// Retained for backward compat. See internal/api/captain.go for deprecation details.
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

const migrationAddCrewTemplatesWorkspaceID = `
ALTER TABLE crew_templates ADD COLUMN workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_crew_templates_workspace ON crew_templates(workspace_id);
`

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

const migrationAddAgentSchedule = `
ALTER TABLE agents ADD COLUMN schedule_cron TEXT;
ALTER TABLE agents ADD COLUMN schedule_prompt TEXT;
ALTER TABLE agents ADD COLUMN schedule_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN schedule_last_run TEXT;
ALTER TABLE agents ADD COLUMN schedule_next_run TEXT;
`

const migrationAddEscalationAction = `
ALTER TABLE escalations ADD COLUMN action TEXT DEFAULT 'approve';
ALTER TABLE escalations ADD COLUMN redirect_to TEXT;
`

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

const migrationAddMCPBindingEnvVar = `
-- Env var name for stdio MCP credential injection (e.g. GITHUB_TOKEN, SLACK_TOKEN)
ALTER TABLE agent_mcp_bindings ADD COLUMN env_var_name TEXT;
`

const migrationAddMCPConfigJSON = `
-- Raw .mcp.json config stored per crew (base) and per agent (additions).
-- Orchestrator merges crew + agent configs at runtime; Claude Code
-- natively expands ${VAR} references from container env vars.
ALTER TABLE crews ADD COLUMN mcp_config_json TEXT;
ALTER TABLE agents ADD COLUMN mcp_config_json TEXT;
`

const migrationAddApprovalGates = `
-- Approval gate columns on mission tasks.
ALTER TABLE mission_tasks ADD COLUMN approval_required INTEGER DEFAULT 0;
ALTER TABLE mission_tasks ADD COLUMN approval_status TEXT;
ALTER TABLE mission_tasks ADD COLUMN approved_by TEXT;
ALTER TABLE mission_tasks ADD COLUMN approved_at TEXT;

-- Tiered escalation config per crew.
ALTER TABLE crews ADD COLUMN escalation_config TEXT;
`

const migrationAddPKCECodeVerifier = `
ALTER TABLE oauth_states ADD COLUMN code_verifier TEXT NOT NULL DEFAULT '';
`

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
