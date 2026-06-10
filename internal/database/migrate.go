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

		// rollback attempts to undo the migration's tx and surfaces any
		// rollback failure (e.g. driver-level connection drop) on the
		// logger. Without this, a Rollback failure that masks the real
		// migration error went completely unlogged.
		rollback := func() {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				logger.Warn("migration rollback failed", "version", m.version, "name", m.name, "error", rbErr)
			}
		}

		// Migrations are either a static SQL string or a Go function — not
		// both. SQL migrations cover the vast majority; fn migrations exist
		// for the rare case where we need to discover schema state at apply
		// time (e.g. iterate pragma_table_info to find legacy columns).
		if m.fn != nil {
			if err := m.fn(ctx, tx, logger); err != nil {
				rollback()
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
		} else {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				rollback()
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO _migrations (version, name) VALUES (?, ?)", m.version, m.name); err != nil {
			rollback()
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
	// DEFAULT need no hook. Hooks MUST be idempotent — see
	// RestoreBackfillFunc for the full contract. See
	// internal/backup/runner_restore.go replayRestoreBackfills.
	restoreBackfill RestoreBackfillFunc
}

// RestoreBackfillFunc is the signature for per-migration hooks that
// populate newly-added columns on rows just restored from an older
// bundle. Each hook runs in its OWN transaction inside the backup
// runner's replayRestoreBackfills loop — that isolation matters for
// the contract below.
//
// # Contract
//
// Hooks MUST be idempotent. This is a hard requirement, not a guideline.
//
// Why: replayRestoreBackfills walks the versions the target has applied
// but the bundle did not, and runs the registered hook for each. Hooks
// run sequentially, each in its own tx. If hook v83 commits successfully
// and hook v84 returns an error, the restore aborts with
// ErrRestoreBackfillFailed — but v83's changes are already on disk.
// The operator's recovery path is to fix the cause and re-run the
// restore; the next run will execute v83 AGAIN against the same rows.
// A non-idempotent hook compounds (e.g. `counter = counter + 1`); the
// second run silently double-counts and data corrupts.
//
// # Recipes for safe idempotency
//
//   - Conditional updates: `UPDATE t SET col = <value> WHERE col IS NULL`
//     — the second run finds nothing to update.
//   - Computed-from-source backfills: `UPDATE t SET derived = f(source)`
//     where f is pure — re-running produces the same result.
//   - Avoid: `UPDATE t SET counter = counter + 1`, `INSERT INTO t (...)`
//     without an ON CONFLICT clause, anything order-dependent.
//
// A returned error aborts the restore but does not roll back the
// already-committed row inserts from the main restore tx — callers
// log loudly and prompt the operator to investigate, but the user-data
// part of the restore is preserved.
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
	// intervention.
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
	// before this migration. Tracked as CRE-128.
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
	// Memory uplift: importance scoring, forgetting via access patterns,
	// and a reward signal from HITL outcomes feeding gate auto-tuning.
	// Three shape changes and one new table, all narrow additions with
	// backwards-compatible defaults so rolling back to migration 53
	// leaves the new columns as orphans the old code simply ignores.
	//
	// - journal_entries.priority: user-facing importance marker. 'normal'
	//   is the default; 'permanent' is never compacted, 'high' boosts
	//   episodic recall weight, 'pin' snapshots into pins.md.
	// - journal_embeddings.importance_score: (0..1) multiplier on cosine
	//   similarity at recall time. Baseline comes from entry_type +
	//   severity; reference_count and last_referenced_at let a nightly
	//   job decay cold entries and boost frequently-recalled ones.
	// - gate_reward_history: records every HITL outcome so
	//   harbormaster.Evaluator can auto-tune gate modes
	//   (approve-rate > 90% → downgrade sync→async; deny-rate > 70% →
	//   upgrade). args_hash is a stable hash of the tool arg shape so
	//   repeated decisions on the same operation cluster together
	//   without storing the raw args (PII + secret hygiene).
	{version: 54, name: "add_memory_importance_and_gate_rewards", sql: `
ALTER TABLE journal_entries ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal';
CREATE INDEX IF NOT EXISTS idx_journal_entries_priority ON journal_entries(workspace_id, priority) WHERE priority != 'normal';

ALTER TABLE journal_embeddings ADD COLUMN importance_score REAL NOT NULL DEFAULT 0.5;
ALTER TABLE journal_embeddings ADD COLUMN reference_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE journal_embeddings ADD COLUMN last_referenced_at TEXT;

CREATE TABLE IF NOT EXISTS gate_reward_history (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK(outcome IN ('approved','denied','timeout','cancelled')),
    decided_by TEXT,
    decided_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    request_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_gate_reward_ws_tool ON gate_reward_history(workspace_id, tool_name, decided_at DESC);
CREATE INDEX IF NOT EXISTS idx_gate_reward_ws_args ON gate_reward_history(workspace_id, tool_name, args_hash, decided_at DESC);
`},
	// Memory quality uplift — five subsystems in one migration because
	// they share plumbing (hybrid retrieval reads the FTS index built
	// for archive-search; health metrics read relations; relations are
	// populated on every embed). Splitting into five migrations would
	// mean five rollbacks-not-done between merges, which makes the
	// half-shipped state the default and the deployed state the rare
	// one.
	//
	// FTS5 virtual table — indexes summary + payload over journal_entries
	// so hybrid recall can run BM25 in parallel with dense cosine. Keep
	// the `tokenize='porter ascii'` so stemming reduces "deploys /
	// deployed / deploying" to one bucket, and ascii so we don't pay the
	// overhead of unicode case folding for an operator-facing tool.
	// `content='journal_entries'` makes the FTS table a shadow of the
	// base table (no duplicate storage); triggers keep them in sync on
	// INSERT / DELETE / UPDATE.
	//
	// journal_entries_archived — compactor moves aged low-value rows here
	// instead of deleting. Same schema as the live table so recall can
	// UNION ALL across both with a simple flag. archived_at records when
	// the compactor moved the row; the original ts stays so timeline
	// queries still work.
	//
	// memory_relations — A-Mem-style graph between entries. relation_kind
	// is a small enum ('similar', 'supports', 'refutes', 'duplicates').
	// `score` is the cosine at insert time for similar; 1.0 for the
	// curated kinds. Union-find over this table computes the Reachability
	// metric in the health snapshot.
	//
	// memory_health_snapshots — daily score per (workspace, crew) so the
	// UI has a monotonic time-series to plot without recomputing five
	// metrics on every request.
	{version: 55, name: "add_memory_quality_uplift", sql: `
CREATE VIRTUAL TABLE IF NOT EXISTS journal_entries_fts USING fts5(
    summary, payload,
    content='journal_entries',
    content_rowid='rowid',
    tokenize='porter ascii'
);
CREATE TRIGGER IF NOT EXISTS journal_entries_ai AFTER INSERT ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(rowid, summary, payload) VALUES (new.rowid, new.summary, new.payload);
END;
-- For external-content FTS5 tables, DELETE/UPDATE triggers use the
-- INSERT(fts, 'delete'/'insert', ...) contentless form. An earlier
-- revision used plain DELETE/INSERT which mutated FTS5's shadow tables
-- directly and corrupted the index (SQLite error 267 "database disk
-- image is malformed"). Keep this form exactly as-is — changes here
-- require a full FTS rebuild.
CREATE TRIGGER IF NOT EXISTS journal_entries_ad AFTER DELETE ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(journal_entries_fts, rowid, summary, payload) VALUES('delete', old.rowid, old.summary, old.payload);
END;
CREATE TRIGGER IF NOT EXISTS journal_entries_au AFTER UPDATE ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(journal_entries_fts, rowid, summary, payload) VALUES('delete', old.rowid, old.summary, old.payload);
    INSERT INTO journal_entries_fts(rowid, summary, payload) VALUES (new.rowid, new.summary, new.payload);
END;

CREATE TABLE IF NOT EXISTS journal_entries_archived (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    archived_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL,
    priority TEXT NOT NULL DEFAULT 'normal',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    compressed_payload TEXT NOT NULL DEFAULT '{}',
    original_size_bytes INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_archived_ws_ts ON journal_entries_archived(workspace_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_archived_ws_type ON journal_entries_archived(workspace_id, entry_type, ts DESC);

CREATE TABLE IF NOT EXISTS memory_relations (
    entry_id TEXT NOT NULL REFERENCES journal_entries(id) ON DELETE CASCADE,
    related_entry_id TEXT NOT NULL REFERENCES journal_entries(id) ON DELETE CASCADE,
    relation_kind TEXT NOT NULL CHECK(relation_kind IN ('similar','supports','refutes','duplicates')),
    score REAL NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY(entry_id, related_entry_id, relation_kind)
);
CREATE INDEX IF NOT EXISTS idx_memory_relations_from ON memory_relations(entry_id, relation_kind);
CREATE INDEX IF NOT EXISTS idx_memory_relations_to ON memory_relations(related_entry_id, relation_kind);

CREATE TABLE IF NOT EXISTS memory_health_snapshots (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    computed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    freshness REAL NOT NULL DEFAULT 0,
    coverage REAL NOT NULL DEFAULT 0,
    coherence REAL NOT NULL DEFAULT 0,
    efficiency REAL NOT NULL DEFAULT 0,
    reachability REAL NOT NULL DEFAULT 0,
    overall REAL NOT NULL DEFAULT 0,
    details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_ws_time ON memory_health_snapshots(workspace_id, computed_at DESC);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_ws_crew ON memory_health_snapshots(workspace_id, crew_id, computed_at DESC);
`},
	// Composite index sized for the /crews Activity-tab SSE stream, which
	// polls journal_entries every 1s with both workspace_id and crew_id
	// bound. Individually we already have (workspace_id, ts DESC) and
	// (crew_id, ts DESC); either alone forces SQLite to filter the other
	// column after the index scan. The composite lets the planner walk
	// a single B-tree ordered by ts for a (workspace_id, crew_id) pair
	// straight out — ~10x fewer row reads per poll on large crews.
	{version: 56, name: "add_journal_ws_crew_ts_index", sql: `
CREATE INDEX IF NOT EXISTS idx_journal_ws_crew_ts ON journal_entries(workspace_id, crew_id, ts DESC);
`},
	// Chat UI overhaul — server-backed branches, reactions, attachments,
	// and workspace-level shared files. Messages themselves live in JSONL
	// files (chats.jsonl_path), so branching uses a sidecar table mapping
	// message_id → parent_id rather than altering the message log.
	{version: 57, name: "add_chat_extras", sql: `
CREATE TABLE IF NOT EXISTS chat_branches (
	id TEXT PRIMARY KEY,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	message_id TEXT NOT NULL,
	parent_id TEXT,
	branch_index INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_branches_chat ON chat_branches(chat_id);
CREATE INDEX IF NOT EXISTS idx_chat_branches_msg ON chat_branches(message_id);
CREATE INDEX IF NOT EXISTS idx_chat_branches_parent ON chat_branches(parent_id);

CREATE TABLE IF NOT EXISTS message_reactions (
	id TEXT PRIMARY KEY,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	message_id TEXT NOT NULL,
	emoji TEXT NOT NULL,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(chat_id, message_id, emoji, user_id)
);
CREATE INDEX IF NOT EXISTS idx_reactions_msg ON message_reactions(message_id);
CREATE INDEX IF NOT EXISTS idx_reactions_chat ON message_reactions(chat_id);

CREATE TABLE IF NOT EXISTS chat_attachments (
	id TEXT PRIMARY KEY,
	chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	filename TEXT NOT NULL,
	mime TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	sha256 TEXT NOT NULL,
	storage_path TEXT NOT NULL,
	uploaded_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_attachments_chat ON chat_attachments(chat_id);
CREATE INDEX IF NOT EXISTS idx_chat_attachments_ws ON chat_attachments(workspace_id);

CREATE TABLE IF NOT EXISTS workspace_files (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	rel_path TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mime TEXT,
	sha256 TEXT,
	created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(workspace_id, rel_path)
);
CREATE INDEX IF NOT EXISTS idx_workspace_files_ws ON workspace_files(workspace_id);
`},
	// Per-user preferences. JSON-blob value keyed by string, scoped by
	// user_id. First consumer is bottom-panel height for the Crews
	// canvas — but the schema is intentionally generic so future UI
	// settings (theme, density, panel positions, last-opened tabs, …)
	// land here without another migration.
	{version: 58, name: "add_user_preferences", sql: `
CREATE TABLE IF NOT EXISTS user_preferences (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	pref_key TEXT NOT NULL,
	pref_value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(user_id, pref_key)
);
CREATE INDEX IF NOT EXISTS idx_user_preferences_user ON user_preferences(user_id);
`},
	// Origin tag on chats — distinguishes sessions started from the
	// chat UI vs `crewship run` CLI vs webhooks vs cron triggers vs
	// agent-to-agent assignments. The SessionsSidebar already renders
	// a chip per origin; this migration is what populates the field.
	// NULL means "unknown / pre-migration" and the UI falls back to
	// not showing a chip — no fake heuristics.
	{version: 59, name: "add_chats_origin", sql: `
ALTER TABLE chats ADD COLUMN origin TEXT;
CREATE INDEX IF NOT EXISTS idx_chats_origin ON chats(origin) WHERE origin IS NOT NULL;
`},
	// Phase D of unified-journal: composite index for the Runs aggregation
	// view. journal.ListRuns groups by trace_id within a workspace; the
	// existing idx_journal_trace is global and forces SQLite to scan the
	// whole table when narrowing to one workspace. The (workspace_id,
	// trace_id) partial index lets the query planner do an index-only
	// range scan keyed on the workspace.
	{version: 60, name: "add_journal_ws_trace_index", sql: `
CREATE INDEX IF NOT EXISTS idx_journal_ws_trace
    ON journal_entries(workspace_id, trace_id)
    WHERE trace_id IS NOT NULL;
`},
	// Phase J of unified-journal: backfill agent_runs into journal_entries
	// (idempotent — skips runs that Phase C already mirrored during the
	// dual-write window), preserve the data in agent_runs_archive, then
	// drop the original table. After this migration, the journal is the
	// single source of truth for runs.
	//
	// The backfill is keyed on trace_id == agent_runs.id so the
	// run.started entry uses the same trace_id the live emit path uses.
	// The NOT EXISTS guard makes this safe to re-run if the migration
	// is replayed (e.g. partial restore).
	{version: 61, name: "drop_agent_runs", sql: `
-- 1) Backfill run.started for every agent_run that doesn't already
--    have a matching journal entry. Use lower(hex(randomblob(8))) for
--    the entry id — collision-free in practice (64 bits) and no
--    extension needed.
INSERT INTO journal_entries
  (id, workspace_id, agent_id, ts, entry_type, severity, actor_type, actor_id,
   summary, payload, refs, trace_id, span_id, expires_at, priority)
SELECT
  'j_' || lower(hex(randomblob(8))),
  r.workspace_id,
  r.agent_id,
  COALESCE(r.started_at, r.created_at),
  'run.started',
  'info',
  CASE WHEN r.triggered_by IS NOT NULL THEN 'user' ELSE 'sidecar' END,
  r.triggered_by,
  'run ' || substr(r.id, 1, 8) || ' started',
  json_object(
    'trigger_type', COALESCE(r.trigger_type, 'USER'),
    'chat_id',      r.chat_id,
    'metadata',     CASE WHEN r.metadata IS NOT NULL AND r.metadata != ''
                         THEN json(r.metadata) ELSE NULL END
  ),
  '{}',
  r.id,
  NULL,
  NULL,
  'normal'
FROM agent_runs r
WHERE NOT EXISTS (
  SELECT 1 FROM journal_entries je
  WHERE je.trace_id = r.id AND je.entry_type = 'run.started'
);

-- 2) Backfill terminal entries for runs that have already finished.
INSERT INTO journal_entries
  (id, workspace_id, agent_id, ts, entry_type, severity, actor_type, actor_id,
   summary, payload, refs, trace_id, span_id, expires_at, priority)
SELECT
  'j_' || lower(hex(randomblob(8))),
  r.workspace_id,
  r.agent_id,
  r.finished_at,
  CASE r.status
    WHEN 'COMPLETED' THEN 'run.completed'
    WHEN 'FAILED'    THEN 'run.failed'
    WHEN 'CANCELLED' THEN 'run.cancelled'
    WHEN 'TIMEOUT'   THEN 'run.timeout'
  END,
  CASE WHEN r.status IN ('FAILED','TIMEOUT') THEN 'error' ELSE 'info' END,
  'sidecar',
  NULL,
  'run ' || substr(r.id, 1, 8) || ' ' || lower(r.status),
  json_object(
    'exit_code',     r.exit_code,
    'error_message', r.error_message
  ),
  '{}',
  r.id,
  NULL,
  NULL,
  'normal'
FROM agent_runs r
WHERE r.status IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT')
  AND r.finished_at IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM journal_entries je
    WHERE je.trace_id = r.id
      AND je.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
  );

-- 3) Snapshot to archive. Idempotent: only insert rows from agent_runs
--    that aren't already in the archive (by id), so re-running the
--    migration via restore replay can't duplicate the snapshot. We
--    don't add a PK here because pre-existing archive rows from a
--    failed previous attempt may already exist with the same ids.
CREATE TABLE IF NOT EXISTS agent_runs_archive AS SELECT * FROM agent_runs WHERE 0;
INSERT INTO agent_runs_archive
  SELECT r.* FROM agent_runs r
  WHERE NOT EXISTS (SELECT 1 FROM agent_runs_archive a WHERE a.id = r.id);

-- 4) Drop the live table and its indexes.
DROP INDEX IF EXISTS idx_run_agent_time;
DROP INDEX IF EXISTS idx_run_workspace;
DROP INDEX IF EXISTS idx_run_status;
DROP INDEX IF EXISTS idx_run_chat;
DROP INDEX IF EXISTS idx_run_triggered_by;
DROP TABLE agent_runs;
`},
	// Subscription-aware paymaster: distinguishes API-key calls (where we can
	// price per token) from OAuth/subscription calls (flat-rate, opaque). Adds
	// rate-card snapshot columns so historical rows survive future rate-card
	// changes, and a confidence column so the UI can label every cost figure
	// with its provenance.
	// Renumbered from v60 to v62 after PR #234 took 60+61 for the unified
	// journal Phase D + drop_agent_runs migrations.
	{version: 62, name: "add_paymaster_billing_modes", sql: migrationAddPaymasterBillingModes},
	// Server-side session evidence backing the access/refresh token
	// model (see internal/auth/jwt.go). A row exists for every issued
	// refresh-token chain; the access token's `sid` claim joins to it
	// on every authenticated request. revoked_at is the kill-switch:
	// signOut, password change, admin force-logout, or refresh rotation
	// flip it and the next request gets 401 session_revoked. Without
	// this table the legacy 30-day JWT was unrevokable until expiry.
	//
	// last_used_at is updated by the auth middleware throttled to
	// at-most-once-per-60-seconds (in-memory cache, not per-request)
	// so the table doesn't take a write hammering on hot endpoints.
	//
	// Originally landed at v60 in the session-lifecycle PR, renumbered
	// to v62 after unified-journal landed v60+v61, then to v63 after
	// the paymaster billing-modes migration took v62 on main. The
	// runner only checks version (not name) — see the migration-version-
	// conflicts pitfall in CLAUDE.md.
	{version: 63, name: "add_user_sessions", sql: `
CREATE TABLE IF NOT EXISTS user_sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT (datetime('now')),
	revoked_at TEXT,
	revoked_reason TEXT,
	user_agent TEXT,
	ip TEXT
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user_active ON user_sessions(user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires ON user_sessions(expires_at);
`},
	// Refresh-token rotation with reuse detection (OWASP ASVS 3.7.4).
	// current_refresh_jti pins the *one* refresh token currently
	// authoritative for the chain. On rotation we mint a new jti and
	// CAS-update this column; if a request comes in carrying an older
	// jti, that's a token-theft signal and the entire session is
	// revoked. failed_login_count + locked_until back the per-account
	// brute-force lockout (E.3 in the security audit) — separate from
	// the per-IP rate limiter so a distributed attacker can't dodge
	// the slow-down by rotating IPs.
	//
	// The partial UNIQUE INDEX on current_refresh_jti enforces the
	// "one live JTI per chain" invariant at the schema layer too, not
	// just in application code (per CodeRabbit review on PR #233):
	// any direct INSERT/UPDATE that tries to point two rows at the
	// same JTI fails at the DB, which keeps a future bug or migration
	// script from silently breaking rotation/reuse detection.
	{version: 64, name: "add_session_rotation_and_lockout", sql: `
ALTER TABLE user_sessions ADD COLUMN current_refresh_jti TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_sessions_current_refresh_jti
  ON user_sessions(current_refresh_jti)
  WHERE current_refresh_jti IS NOT NULL;
ALTER TABLE users ADD COLUMN failed_login_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until TEXT;
ALTER TABLE users ADD COLUMN last_failed_login_at TEXT;
`},
	// v65 — from main (PR #267 skills bootstrap). Kept ahead of the
	// connections migrations so the version sequence matches main; our
	// connections migrations were renumbered v65→v73 during the
	// feat/connections ↔ main merge to avoid the version-collision
	// guard in Migrate().
	//
	// vendor namespaces a skill (e.g. "anthropic", "vercel", "community") so
	// future workspace templates can reference skills as vendor/slug@version
	// without colliding when two registries publish the same slug. Kept
	// nullable for the v0.1 migration — backfill happens lazily as bundled
	// skills upsert at startup with their canonical vendor.
	//
	// runtime distinguishes pure-instruction skills (the SKILL.md body is the
	// payload) from script-bundle skills, MCP-wrapping skills, and hybrids.
	// Drives the "Runtime" facet on the browse page and the per-CLI adapter
	// decision (MCP skills go to .mcp.json instead of a skills/ folder).
	//
	// maturity replaces the brittle stars/downloads proxy used by other
	// registries. OFFICIAL = vendor-published; CURATED = vetted by Crewship;
	// COMMUNITY = imported by users; EXPERIMENTAL = explicitly marked WIP.
	//
	// scan_status records the outcome of the prompt-injection regex heuristic
	// (built-in, runs on every import) plus the optional snyk-agent-scan
	// shell-out. BLOCKED skills are present in the table but excluded from
	// install candidates until cleared.
	//
	// spdx_license is the canonical SPDX identifier (parsed from frontmatter
	// or detected from a sibling LICENSE file). Distinct from the freeform
	// `license` column so the SPDX allowlist gate has a stable key to filter
	// on.
	{version: 65, name: "add_skill_bootstrap_fields", sql: `
ALTER TABLE skills ADD COLUMN vendor TEXT;
ALTER TABLE skills ADD COLUMN homepage TEXT;
ALTER TABLE skills ADD COLUMN spdx_license TEXT;
ALTER TABLE skills ADD COLUMN runtime TEXT NOT NULL DEFAULT 'INSTRUCTIONS';
ALTER TABLE skills ADD COLUMN maturity TEXT NOT NULL DEFAULT 'COMMUNITY';
ALTER TABLE skills ADD COLUMN scan_status TEXT NOT NULL DEFAULT 'UNSCANNED';
ALTER TABLE skills ADD COLUMN description_quality TEXT;
CREATE INDEX IF NOT EXISTS idx_skill_vendor ON skills(vendor);
CREATE INDEX IF NOT EXISTS idx_skill_maturity ON skills(maturity);
CREATE INDEX IF NOT EXISTS idx_skill_runtime ON skills(runtime);
`},
	// v66 backs the row-level "is this credential still in use?" signal
	// copied from GitLab/GitHub/Stripe. last_checked_at already exists
	// for health-check timestamps; last_used_at is distinct — it records
	// real usage as observed by the sidecar and is the input for the
	// computed Stale status (last_used_at < now - 90d) surfaced in the
	// 5-state taxonomy from CONNECTIONS.md §3.4. last_used_ips holds a
	// JSON array max 5 elements; the ringbuffer cap is enforced in Go,
	// not the schema. We do NOT add a separate expires_at column —
	// token_expires_at on credentials already covers that and adding a
	// duplicate would split writes across two columns and rot one of
	// them. PRD CONNECTIONS.md mentioned expires_at as shorthand; this
	// migration formalises the column reuse decision.
	{version: 66, name: "add_credential_audit_signal", sql: `
ALTER TABLE credentials ADD COLUMN last_used_at TEXT;
ALTER TABLE credentials ADD COLUMN last_used_ips TEXT;
CREATE INDEX IF NOT EXISTS idx_credentials_last_used ON credentials(last_used_at);
`},
	// v67 adds mcp_tool_bindings for the per-tool enable/disable feature
	// (Cursor parity, biggest MVP differentiator vs. Cursor/Continue per
	// CONNECTIONS.md §3.1). One MCP server publishes N tools via
	// mcp/list-tools; agents bound to that server see the union of
	// enabled tools. mcp_server_id+mcp_server_scope mirrors the
	// agent_mcp_bindings discriminator pattern (workspace_mcp_servers vs
	// crew_mcp_servers live in separate ID spaces). description is
	// optional — populated when the sidecar refreshes from the live
	// server, NULL when the row was created manually.
	{version: 67, name: "add_mcp_tool_bindings", sql: `
CREATE TABLE IF NOT EXISTS mcp_tool_bindings (
	id TEXT PRIMARY KEY,
	mcp_server_id TEXT NOT NULL,
	mcp_server_scope TEXT NOT NULL CHECK(mcp_server_scope IN ('workspace','crew')),
	tool_name TEXT NOT NULL,
	description TEXT,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(mcp_server_id, mcp_server_scope, tool_name)
);
CREATE INDEX IF NOT EXISTS idx_mcp_tool_bindings_server ON mcp_tool_bindings(mcp_server_id, mcp_server_scope);
`},
	// v68 promotes the existing binary is_verified flag on
	// mcp_registry_servers (added in v36) to the 3-tier trust model
	// from CONNECTIONS.md §5.6: anthropic / crewship / community.
	// is_featured is a curation-only flag (DO-NOT-BUILD #4: install
	// counts must be real or absent — featured replaces them as a
	// trust signal we control without faking metrics). The sync
	// worker (SyncMCPRegistry) treats both columns as locally-owned
	// and never overwrites them on upsert, so manual curation
	// survives every sync cycle. Backfill: every existing entry
	// synced from the upstream Anthropic registry gets
	// trust_tier='anthropic' (that's what is_verified meant before),
	// and is_featured stays 0 until an admin promotes it.
	{version: 68, name: "add_mcp_registry_trust_tier", sql: `
ALTER TABLE mcp_registry_servers ADD COLUMN trust_tier TEXT NOT NULL DEFAULT 'community';
ALTER TABLE mcp_registry_servers ADD COLUMN is_featured INTEGER NOT NULL DEFAULT 0;
UPDATE mcp_registry_servers SET trust_tier = 'anthropic' WHERE is_verified = 1;
CREATE INDEX IF NOT EXISTS idx_mcp_registry_trust_tier ON mcp_registry_servers(trust_tier);
CREATE INDEX IF NOT EXISTS idx_mcp_registry_featured ON mcp_registry_servers(is_featured) WHERE is_featured = 1;
`},
	// v69 backs the inline audit drawer (CONNECTIONS.md §4.3 Audit
	// tab — Doppler pattern: per-row slide-out > separate audit page).
	// Each event captures who used the credential, from which IP, and
	// the outcome. event_type is open-ended (USE / ROTATE / TEST /
	// REVOKE / DETECTED) — a CHECK constraint here would force a
	// schema migration every time we add a new event class, so we
	// validate at the Go layer instead. metadata_json carries
	// event-specific context (e.g. {"old_status":"ACTIVE",
	// "new_status":"ERROR","reason":"401 Unauthorized"}).
	//
	// agent_id is nullable: rotations and test-connection events
	// originate from a user request, not an agent run.
	//
	// The (credential_id, occurred_at DESC) index is the hot path —
	// the detail Sheet's Audit tab queries last 50 events per
	// credential.
	{version: 69, name: "add_credential_audit", sql: `
CREATE TABLE IF NOT EXISTS credential_audit (
	id TEXT PRIMARY KEY,
	credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
	event_type TEXT NOT NULL,
	agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
	ip_address TEXT,
	metadata_json TEXT,
	occurred_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_credential_audit_credential ON credential_audit(credential_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_credential_audit_occurred ON credential_audit(occurred_at);
`},
	// v70 backs the rotation-with-grace-overlap feature — biggest
	// enterprise differentiator vs. GitLab's "original becomes
	// inactive immediately" pattern (CONNECTIONS.md §7.1, MUST-add #1
	// from project_credentials_integrations_strategy.md).
	//
	// On rotate: a row is inserted with both encrypted values. The
	// credentials.encrypted_value column flips to the new value
	// immediately (so all NEW agent runs use the new key) but the
	// sidecar can fall back to old_value during the grace window if
	// it hits a 401 — covers in-flight runs that had already cached
	// the old value at start.
	//
	// status transitions: ACTIVE → EXPIRED (grace ran out, old_value
	// scrubbed) or ACTIVE → CANCELLED (admin clicked "End grace
	// early"). Both terminals scrub old_value to ''.
	//
	// The (expires_at, status) index supports the hourly cron that
	// finds rotations to expire.
	{version: 70, name: "add_credential_rotations", sql: `
CREATE TABLE IF NOT EXISTS credential_rotations (
	id TEXT PRIMARY KEY,
	credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
	old_value TEXT NOT NULL,
	grace_seconds INTEGER NOT NULL DEFAULT 0,
	rotated_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT NOT NULL,
	rotated_by TEXT NOT NULL REFERENCES users(id),
	status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','EXPIRED','CANCELLED'))
);
CREATE INDEX IF NOT EXISTS idx_credential_rotations_credential ON credential_rotations(credential_id, rotated_at DESC);
CREATE INDEX IF NOT EXISTS idx_credential_rotations_expires ON credential_rotations(expires_at, status);
`},
	// v71 fixes the credential_rotations.rotated_by FK from
	// "NOT NULL REFERENCES users(id)" (defaults to NO ACTION) to
	// "REFERENCES users(id) ON DELETE SET NULL". The original v70
	// would block deleting any user who has rotation history with a
	// FK constraint error — incompatible with how credential_audit
	// already nulls out agent_id on agent deletion.
	//
	// SQLite can't ALTER an existing FK, so this is the standard
	// recreate dance: rename old → create new with fixed schema →
	// copy rows → drop old. Indexes are recreated afterwards.
	{version: 71, name: "fix_credential_rotations_rotated_by_fk", sql: `
CREATE TABLE credential_rotations_new (
	id TEXT PRIMARY KEY,
	credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
	old_value TEXT NOT NULL,
	grace_seconds INTEGER NOT NULL DEFAULT 0,
	rotated_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT NOT NULL,
	rotated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','EXPIRED','CANCELLED'))
);
INSERT INTO credential_rotations_new (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
	SELECT id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status FROM credential_rotations;
DROP TABLE credential_rotations;
ALTER TABLE credential_rotations_new RENAME TO credential_rotations;
CREATE INDEX IF NOT EXISTS idx_credential_rotations_credential ON credential_rotations(credential_id, rotated_at DESC);
CREATE INDEX IF NOT EXISTS idx_credential_rotations_expires ON credential_rotations(expires_at, status);
`},
	// v72 adds a free-form tag list to credentials so users can organise
	// secrets without our hardcoded provider enum getting in the way
	// (Doppler / Vercel pattern). Stored as JSON TEXT — SQLite has no
	// native array type, and pulling tags into a junction table is
	// premature for a list that's typically 0–3 items per credential.
	{version: 72, name: "add_credential_tags", sql: `
ALTER TABLE credentials ADD COLUMN tags TEXT;
`},
	// v73 adds a composite index that backs the crew-scoped credential
	// visibility filter (credentialVisibilityFilter in
	// internal/api/credentials_loaders.go). The existing
	// idx_crew_member_user is sufficient for a probe by user_id, but
	// the EXISTS subquery joins onto credential_crews on crew_id —
	// having both columns in the same index lets SQLite serve the join
	// index-only at scale. Acceptable to skip on small workspaces, but
	// the ordering of the existing single-column indexes already
	// implies this access pattern, so we add it explicitly.
	{version: 73, name: "add_crew_members_composite_index", sql: `
CREATE INDEX IF NOT EXISTS idx_crew_member_user_crew ON crew_members(user_id, crew_id);
`},
	// v74 — auto-backup scheduling + off-site destinations.
	// Originally authored as v65 on this branch; renumbered during the
	// merge with main after PR #267 (skills) and PR #269 (connections)
	// claimed v65–v73. The migration runner keys on (version, name) so
	// a renumbered migration is treated as a fresh apply on every
	// existing database that already ran the connections/skills
	// chain — which is exactly what we want here, because no instance
	// has applied the original v65 yet.
	//
	// scheduled_jobs holds cron-driven backup tasks. A row's lifecycle:
	//   created → next_run_at computed by gocron → fires → last_run_at +
	//   last_status updated. scope+target_id mirror the existing manual
	//   POST /api/v1/admin/backups payload so the runner can reuse one
	//   code path. destination_id NULL = local default (back-compat with
	//   pre-migration manual backups).
	//
	// backup_destinations is the registry of where bundles can be written.
	// kind=local writes to local_path; the S3-family kinds are all reached
	// via minio-go/v7 (single client lib, all S3-compatible APIs). Secrets
	// support env: prefix (recommended) so the access key never lands in
	// the DB cleartext; inline values are still allowed for ergonomic
	// single-tenant setups but should be wrapped via age in a follow-up.
	//
	// One-row-per-(workspace,name) UNIQUE prevents accidental duplicate
	// destination registration. Restore handler will refuse to delete a
	// destination that any scheduled_job still references.
	{version: 74, name: "add_backup_schedules_and_destinations", sql: `
CREATE TABLE IF NOT EXISTS backup_destinations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('local','s3','b2','wasabi','minio','r2')),
    bucket TEXT,
    region TEXT,
    endpoint TEXT,
    path_style INTEGER NOT NULL DEFAULT 0,
    prefix TEXT NOT NULL DEFAULT '',
    access_key_ref TEXT,
    secret_key_ref TEXT,
    local_path TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    last_tested_at TEXT,
    last_test_status TEXT CHECK(last_test_status IS NULL OR last_test_status IN ('reachable','unreachable','error')),
    last_test_error TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, name)
);
CREATE INDEX IF NOT EXISTS idx_backup_destinations_workspace ON backup_destinations(workspace_id);

CREATE TABLE IF NOT EXISTS scheduled_jobs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind TEXT NOT NULL DEFAULT 'backup' CHECK(kind IN ('backup')),
    name TEXT NOT NULL,
    cron_expr TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    scope TEXT NOT NULL CHECK(scope IN ('workspace','crew','config')),
    target_id TEXT,
    keep_last INTEGER NOT NULL DEFAULT 14,
    keep_days INTEGER NOT NULL DEFAULT 30,
    destination_id TEXT REFERENCES backup_destinations(id) ON DELETE SET NULL,
    encryption_mode TEXT NOT NULL DEFAULT 'passphrase' CHECK(encryption_mode IN ('none','passphrase','age_recipients')),
    enabled INTEGER NOT NULL DEFAULT 1,
    notify_on_failure INTEGER NOT NULL DEFAULT 0,
    last_run_at TEXT,
    last_status TEXT CHECK(last_status IS NULL OR last_status IN ('success','failed','skipped')),
    last_error TEXT,
    last_bundle_path TEXT,
    next_run_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_workspace ON scheduled_jobs(workspace_id, enabled);
CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_next_run ON scheduled_jobs(enabled, next_run_at) WHERE enabled = 1;
`},
	// v75 — was v66 before the merge. scope_level lets the admin UI
	// render the "Quick / Standard / Full" preset badge alongside an
	// existing bundle without having to inspect each manifest. Pre-v75
	// rows backfill to 'standard' (what the collector did when no
	// preset existed) so existing entries don't render an empty badge.
	{version: 75, name: "add_backup_catalog_scope_level", sql: `
ALTER TABLE backup_catalog ADD COLUMN scope_level TEXT NOT NULL DEFAULT 'standard';
`},
	// v76 — link an installed MCP server back to the catalog manifest
	// it was created from. Nullable so existing rows (created before
	// the catalog existed) and rows from the "Custom MCP server"
	// escape hatch (no manifest) stay legal. Index is partial — we
	// only ever query rows where the column is set.
	//
	// connector_id stores the manifest id (e.g. "linear", "github"),
	// NOT a foreign-key reference: manifests live in code (embed.FS),
	// not the database. Renames or removals from the catalog must be
	// handled at the application layer (e.g. drift report on startup).
	{version: 76, name: "add_workspace_mcp_connector_id", sql: `
ALTER TABLE workspace_mcp_servers ADD COLUMN connector_id TEXT;
ALTER TABLE crew_mcp_servers ADD COLUMN connector_id TEXT;
CREATE INDEX IF NOT EXISTS idx_workspace_mcp_connector_id ON workspace_mcp_servers(connector_id) WHERE connector_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_crew_mcp_connector_id ON crew_mcp_servers(connector_id) WHERE connector_id IS NOT NULL;
`},
	// New filter shapes hit by the unified-journal expansion need
	// indexes to stay snappy at workspace scale (10k+ entries/day):
	//   trace_id  → /journal?trace_id=… deeplink and the click-through
	//               from RunsView; partial index skips the bulk of
	//               container.metrics rows that have no trace.
	//   actor_type→ "show me what users did vs agents" filter chip.
	//   priority  → "permanent / pin / high" surfaces in compaction
	//               + recall paths; skipping the dominant 'normal'
	//               value via the partial predicate.
	{version: 77, name: "add_journal_filter_indexes", sql: `
CREATE INDEX IF NOT EXISTS idx_journal_trace_id ON journal_entries(trace_id) WHERE trace_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_journal_actor_ts ON journal_entries(actor_type, ts DESC);
CREATE INDEX IF NOT EXISTS idx_journal_priority ON journal_entries(priority) WHERE priority != 'normal';
`},
	// v78 introduces pipelines — declarative DSL documents persisted
	// per-workspace, authored by AI agents (or users) and reusable
	// across crews via the [AVAILABLE PIPELINES] system-prompt block.
	// See migrate_consts_v78_pipelines.go for the table DDL.
	{version: 78, name: "add_pipelines", sql: migrationAddPipelines},
	// v79 adds pipeline versioning + waitpoints. Versioning makes
	// the in-place edits from v78 immutable history (each save = new
	// row, head_version pointer on pipelines), so rollback + audit
	// + marketplace integrity are real. Waitpoints persist
	// approval-step tokens so a wait survives process restarts.
	{version: 79, name: "add_pipeline_versions_and_waitpoints", sql: migrationAddPipelineVersionsAndWaitpoints},
	// v80 adds pipeline_schedules — cron-like triggers that fire
	// a saved pipeline on a recurring schedule. Closes Pavel's
	// "every day at 8 fetch email and summarize" use case without
	// requiring an agent in the loop. See migrate_consts_v80*.go.
	{version: 80, name: "add_pipeline_schedules", sql: migrationAddPipelineSchedules},
	// v81 adds pipeline_run_idempotency — webhook redelivery dedupe.
	// Cancel + concurrency state stays in-process (in-memory run
	// registry) because both couple to Go contexts; only the
	// idempotency reservation is durable. See migrate_consts_v81*.go.
	{version: 81, name: "add_pipeline_run_support", sql: migrationAddPipelineRunSupport},
	// v82 adds pipeline_webhooks — event-driven trigger surface
	// alongside cron schedules. Closes the "Stripe webhook fires
	// pipeline" use case. See migrate_consts_v82*.go.
	{version: 82, name: "add_pipeline_webhooks", sql: migrationAddPipelineWebhooks},
	// v83 promotes routine runs from journal-only to a dedicated
	// pipeline_runs table so list-active-runs becomes B-tree scan
	// and boot recovery has somewhere to mark interrupted in-flight
	// runs. Closes the "restart loses runs" production gap from
	// PIPELINES.md §17.6. See migrate_consts_v83*.go.
	{version: 83, name: "add_pipeline_runs", sql: migrationAddPipelineRuns},
	// v84 binds issues to routines. Adds missions.routine_id +
	// missions.routine_inputs_json so an issue can carry a "this is
	// the routine that handles me" pointer + the inputs to invoke
	// it with. Closes the gap where issues were free text + assignee
	// with no path to "automate this." See migrate_consts_v84*.go.
	{version: 84, name: "add_issue_routine_binding", sql: migrationAddIssueRoutineBinding},
	// v85 introduces the unified inbox_items table — one row per
	// "thing that needs the human." Backfills currently-open
	// waitpoints + escalations so the inbox lights up on first
	// deploy. Future kinds (failed runs, agent messages) drop in
	// via the kind discriminator. See migrate_consts_v85*.go.
	{version: 85, name: "add_inbox_items", sql: migrationAddInboxItems},
	// v86 adds a purpose discriminator to verification_tokens
	// (existing email_verify rows backfill in place) so the same
	// table can carry password-reset tokens, plus a cli_pairings
	// table for the device-code handoff flow that lets users pair a
	// local CLI (Claude Code, Gemini, Codex, OpenCode, Cursor,
	// Factory Droid — adapter-agnostic) without copying a session
	// token through their shell history. See migrate_consts_v86*.go.
	{version: 86, name: "add_recovery_and_pairing", sql: migrationAddRecoveryAndPairing},
	// v87 backfills composite (workspace_id, created_at DESC) indexes
	// on the dominant workspace-scoped list endpoints. The pre-existing
	// single-column workspace indexes cover the WHERE predicate but
	// force a separate sort for the ORDER BY created_at DESC pagination
	// the dashboard + every list view runs; v53's eval_runs index is
	// the proven precedent. The partial WHERE deleted_at IS NULL keeps
	// the index size proportional to live rows for soft-deletable
	// tables (agents/crews/credentials/pipelines), matching the
	// WHERE clauses that handlers already issue. chats has no
	// deleted_at column, so its index is unconditional.
	{version: 87, name: "add_workspace_created_indexes", sql: `
CREATE INDEX IF NOT EXISTS idx_agents_ws_created ON agents(workspace_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crews_ws_created ON crews(workspace_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_missions_ws_created ON missions(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_chats_ws_created ON chats(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_credentials_ws_created ON credentials(workspace_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pipelines_ws_created ON pipelines(workspace_id, created_at DESC) WHERE deleted_at IS NULL;
`},
	// Generic single-row key/value store for instance-wide settings that
	// don't fit any existing domain table. Currently used by the telemetry
	// subsystem to record:
	//   - telemetry_opt_in     ("0" | "1")  — operator-chosen
	//   - telemetry_install_id (uuid)       — generated on first opt-in,
	//                                          identifies the install for
	//                                          crash-grouping without leaking
	//                                          anything user-identifying.
	// The opt-in row's absence is treated as "not asked yet" so cmd_start
	// can prompt at first boot.
	{version: 88, name: "add_app_settings", sql: `
CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- Touch updated_at on every value mutation. The column DEFAULT only
-- fires on INSERT; without this trigger an UPDATE that forgets to set
-- updated_at would leave the stamp stale (CodeRabbit caught this on
-- review). Today only crashreport.upsertSetting writes the table and it
-- does set updated_at, but DB-level enforcement is the right place for
-- the rule so a future caller can't drift.
CREATE TRIGGER IF NOT EXISTS trg_app_settings_touch_updated_at
AFTER UPDATE OF value ON app_settings
FOR EACH ROW
BEGIN
    UPDATE app_settings
       SET updated_at = datetime('now')
     WHERE key = OLD.key;
END;
`},
	// v89 closes two FK gaps the v01 init shipped with:
	//   - chats.created_by → users(id): had no ON DELETE clause, so
	//     deleting a user FAILED with FK violation (NO ACTION is the
	//     SQLite default) even though the column is nullable. Intent
	//     per Prisma schema is SET NULL — account deletion should not
	//     block on historical chat ownership.
	//   - assignments.chat_id → chats(id): same root cause. Deleting
	//     a chat with sidebar assignments failed FK. Intent is CASCADE
	//     so the chat delete sweeps its assignments.
	//
	// The "natural" SQLite recipe — recreate the table with the new FK
	// clause and swap — does NOT work inside Migrate()'s wrapper
	// transaction on a populated database. DROP TABLE chats fires the
	// existing assignments NO-ACTION FK and queues a deferred violation
	// that COMMIT then refuses, even though the rename restores
	// referential integrity within the same transaction. SQLite's
	// official workaround (PRAGMA foreign_keys=OFF *outside* the tx,
	// then foreign_key_check before COMMIT) can't be applied because
	// foreign_keys can only be toggled in autocommit mode, and the
	// migration framework already holds the tx.
	//
	// BEFORE-DELETE triggers achieve the same observable semantics
	// without touching the FK clauses: clearing/removing the dependent
	// rows first means the parent delete's implicit FK check finds
	// nothing to block on. No data moves, no table is recreated, so
	// the migration is safe on populated beta installs. Idempotent
	// via DROP TRIGGER IF EXISTS.
	{version: 89, name: "add_chat_assignment_cascade_triggers", sql: `
-- chats.created_by → users(id): emulate ON DELETE SET NULL.
DROP TRIGGER IF EXISTS trg_chats_creator_set_null_on_user_delete;
CREATE TRIGGER trg_chats_creator_set_null_on_user_delete
BEFORE DELETE ON users
FOR EACH ROW
BEGIN
    UPDATE chats SET created_by = NULL WHERE created_by = OLD.id;
END;

-- assignments.chat_id → chats(id): emulate ON DELETE CASCADE.
DROP TRIGGER IF EXISTS trg_assignments_cascade_on_chat_delete;
CREATE TRIGGER trg_assignments_cascade_on_chat_delete
BEFORE DELETE ON chats
FOR EACH ROW
BEGIN
    DELETE FROM assignments WHERE chat_id = OLD.id;
END;
`},
	// v90 introduces the HITL staging surface for the memory
	// consolidator (memory_proposals table) plus widens the
	// inbox_items.kind CHECK constraint to admit 'memory_consolidation'
	// inbox items, plus adds a per-workspace memory_config JSON column
	// on workspaces for fine-grained scrubber / cap / watcher overrides.
	// See migrate_consts_v90_memory_proposals.go for full design notes.
	// (Originally authored as v89 in feat/memory-reliability-bundle;
	// renumbered to v90 on rebase because the cascade-triggers
	// migration above took v89 in main first. Migration bodies are
	// independent — both apply cleanly in either order.)
	{version: 90, name: "add_memory_proposals", sql: migrationAddMemoryProposals},
	// v91 introduces the memory_versions audit table for every
	// memory.WriteFile call — append-only, content-addressed blob
	// references on disk under {memoryRoot}/versions/{sha[:2]}/{sha}.
	// Satisfies EU AI Act Art. 14 oversight requirements (enforcement
	// Aug 2 2026) and matches the immutable-versions pattern Anthropic
	// Managed Agents shipped April 2026. See
	// migrate_consts_v91_memory_versions.go for full design notes.
	// (Originally authored as v90 in PR #2; renumbered to v91 on rebase
	// because the renumbered add_memory_proposals took v90 first.)
	{version: 91, name: "add_memory_versions", sql: migrationAddMemoryVersions},
	// v92 adds score_json TEXT column to memory_proposals so the
	// consolidator persists the six-signal scoring breakdown
	// (relevance / frequency / query diversity / recency /
	// consolidation / conceptual richness) alongside each proposal.
	// The explain endpoint surfaces it back to operators for HITL
	// review. See migrate_consts_v92_proposal_scoring.go.
	// (Originally authored as v91 in PR #4; renumbered to v92 on rebase
	// because PR #2's renumbered add_memory_versions took v91 first.)
	{version: 92, name: "add_proposal_scoring", sql: migrationAddProposalScoring},
	// v93 lays the per-crew admission-queue schema:
	//   - assignments.queued_at + assignments.running_at columns
	//   - crews.max_concurrent_agents column (NULL = compute from
	//     container_memory_mb)
	//   - partial index on assignments(status, queued_at) WHERE
	//     status='QUEUED' for the queue-pump read path.
	// See internal/database/migrate_consts_v93_assignment_queue.go
	// and .claude/context/prd/QUEUE-MECHANISM-2026.md.
	{version: 93, name: "add_assignment_queue", sql: migrationAddAssignmentQueue},
	// v94 extends credentials + agent_credentials to support the
	// USERPASS, SSH_KEY, CERTIFICATE, and GENERIC_SECRET vault types.
	// Adds credentials.username (cleartext identifier, like Bitwarden's
	// login.username field) and agent_credentials.mount_type to
	// discriminate env-var injection from in-container file mounts.
	// (Originally authored as v93; renumbered to v94 on rebase because
	// PR #395's add_assignment_queue took v93 first.)
	// See migrate_consts_v94_credential_vault_types.go.
	{version: 94, name: "add_credential_vault_types", sql: migrationAddCredentialVaultTypes},
	// v95: stores sidecar service declarations (Redis/Postgres/etc.)
	// alongside the crew's other provisioning config. See
	// migrate_consts_v95_crew_services.go for the rationale.
	{version: 95, name: "add_crew_services", sql: migrationAddCrewServices},
	// Typed per-message feedback (thumbs/edit/regenerate/abandon) bound to
	// trace_id for ADLC phase-7 continuous-learning signal. See
	// migrate_consts_v96_message_feedback.go.
	// (Renumbered from v95 to v96 on rebase because main's
	// add_crew_services took v95 first.)
	{version: 96, name: "add_message_feedback", sql: migrationAddMessageFeedback},
	// Online eval sampler — widens eval_runs.kind to include 'online'
	// and adds routine_slug + pipeline_run_id + trace_id + sample_rate
	// so production traffic can be continuously graded. SQLite recreate
	// pattern (CHECK constraints are immutable in place).
	// See migrate_consts_v97_eval_runs_online.go.
	// (Renumbered from v96 to v97 on rebase for the same reason.)
	{version: 97, name: "eval_runs_online", sql: migrationEvalRunsOnline},

	// Credential attribution: who created the row + what sidecar
	// service (if any) it belongs to. Backfills as 'user' for every
	// pre-v98 credential, so behaviour is unchanged until the apply
	// dispatch starts tagging AUTO_MANAGED rows with 'agent'.
	// See migrate_consts_v98_credential_attribution.go. (Landed on
	// main as PR #460 while this branch was open.)
	{version: 98, name: "credential_attribution", sql: migrationAddCredentialAttribution},

	// Two-tier CLI tokens (Patch J): adds `tier` ('STANDARD' | 'ADMIN')
	// and `expires_at` to cli_tokens, plus a cli_token_uses audit
	// table for ADMIN tier per-use logging. Existing rows backfill to
	// tier='STANDARD' with NULL expires_at — full backwards compat.
	// Originally drafted as v98 on this branch; rebased to v99 when
	// PR #460 landed credential_attribution as v98 on main.
	// See migrate_consts_v99_cli_token_tiers.go.
	{version: 99, name: "cli_token_tiers", sql: migrationCLITokenTiers},

	// RBAC extensions (Patch M): per-crew role override on
	// crew_members, per-agent owner on agents, optional scope list
	// on cli_tokens. Additive — every change has NULL/default that
	// preserves pre-v100 behaviour for existing rows. Rebased from
	// v99 to v100 in the same renumber pass as cli_token_tiers.
	// See migrate_consts_v100_rbac_extensions.go.
	{version: 100, name: "rbac_extensions", sql: migrationRBACExtensions},

	// Per-crew autonomy policy (PRD §6 F2 / PR-B): autonomy_level
	// + behavior_mode + audit triple. Net-new columns with column-
	// level CHECK constraints; no recreate dance. Originally drafted
	// as v98 / v99 in earlier rebases; bumped to v101 after main
	// landed v99 (cli_token_tiers) + v100 (rbac_extensions).
	// See migrate_consts_v101_autonomy.go.
	{version: 101, name: "autonomy", sql: migrationAutonomy},

	// Keeper Phase 2 (PRD §6 F4 / PR-C): widens keeper_requests.request_
	// type to a CHECK-constrained enum admitting four new kinds
	// (skill_review, behavior, memory_health, negative_learning) and
	// lands the skills lifecycle columns + skill_invocations audit
	// table the F4.1 evaluator consumes.
	// See migrate_consts_v102_keeper_phase2.go.
	{version: 102, name: "keeper_phase2", sql: migrationKeeperPhase2},

	// Ephemeral agent lifecycle (PRD §6 F5 / PR-D): five additive
	// columns on agents (ephemeral / expires_at / expired_at /
	// parent_lead_id / hire_reason) + per-crew quota
	// (crews.max_ephemeral_agents). Powers the hire / ghost / rehire
	// triple — see migrate_consts_v103_ephemeral_agents.go for the
	// column-by-column rationale.
	{version: 103, name: "ephemeral_agents", sql: migrationEphemeralAgents},

	// PR-E F6 (PRD §6 F6) — PERSONA + per-user peer cards + GDPR.
	// v104 lands two related schema changes (rename + tier widen)
	// that must apply together: PERSONA.md content flows from
	// agents.system_prompt → system_prompt_legacy on first write,
	// AND the memory_versions audit table must accept the new
	// 'persona' / 'peer' tiers so the very first PERSONA write is
	// recordable. See migrate_consts_v104_persona_rename.go.
	// Originally numbered v102 on this branch; renumbered to v104 on
	// rebase after main landed PR-C's v102 + PR-D's v103.
	{version: 104, name: "persona_rename", sql: migrationPersonaRename},

	// v105: GDPR primitives for per-user peer cards. user_peer_
	// consent gates the writer, peer_cards is the disk-mirror index
	// that powers list/delete endpoints, peer_card_audit records
	// every write/read/delete keyed by data subject for SAR
	// fulfilment. See migrate_consts_v105_peer_consent.go.
	// Renumbered from v103 on rebase past PR-D's v103.
	{version: 105, name: "peer_consent", sql: migrationPeerConsent},

	// v106: per-agent self-learning posture (PR-G F4.1 UX). When
	// flipped on the keeper evaluators may auto-promote ALLOW
	// proposals (skill activate, lesson land) without an inbox
	// approval; OFF keeps governance-first behavior. Still subordinate
	// to the crew's autonomy_level — strict crews can't self-learn.
	// See migrate_consts_v106_self_learning.go.
	{version: 106, name: "self_learning", sql: migrationSelfLearning},

	// v107 (PR-F F6): GDPR cascade primitives. Adds data_subject_id
	// pointer columns to memory_versions + inbox_items (peer_cards
	// already has user_id from v105) so the admin SAR endpoints can
	// enumerate everything we hold about a single user, and the
	// gdpr_actions audit table that records every Art. 15 (access)
	// or Art. 17 (erasure) invocation. keeper_requests intentionally
	// excluded — its rows are agent/crew/credential scoped, no user-
	// attributable content. See migrate_consts_v107_gdpr_cascade.go.
	{version: 107, name: "gdpr_cascade", sql: migrationGDPRCascade},
	{version: 108, name: "mission_provenance", sql: migrationMissionProvenance},

	// v109 (PRD-SLASH-CAPABILITIES-2026 §6.1): per-membership
	// capability set on workspace_members. Layers on top of the v100
	// role tier model — capability strings are workspace-scoped JSON
	// in TEXT (same shape as cli_tokens.scopes). Backfill writes
	// role-aware bundles so single-operator OWNER installs upgrade
	// without losing surface. See migrate_consts_v109_member_capabilities.go.
	{version: 109, name: "member_capabilities", sql: migrationMemberCapabilities},

	// v110: partial unique index enforcing at-most-one-LEAD-per-crew at
	// the DB level, closing the check-then-act TOCTOU race in the agent
	// create + promote paths. Partial on (agent_role='LEAD' AND
	// deleted_at IS NULL) so AGENT rows and soft-deleted leads are
	// unconstrained. See migrate_consts_v110_one_lead_per_crew.go.
	{version: 110, name: "one_lead_per_crew", sql: migrationOneLeadPerCrew},

	// v111: cross-session conversation search. Adds conversation_messages
	// (a queryable mirror of the JSONL chat logs) and its external-content
	// FTS5 shadow conversation_messages_fts. The conversation Store
	// dual-writes a row here on every Append; Search filters ALWAYS by
	// agent_id. First slice is BM25-only with no backfill of pre-v111
	// history. See migrate_consts_v111_conversation_search.go.
	{version: 111, name: "add_conversation_search", sql: migrationConversationSearch},

	// v112: index table for the evolving per-user operator model
	// (PR #10 F6). Mirrors peer_cards but keyed on (workspace_id,
	// user_slug) alone — no agent_id, because the model is per
	// operator, not per (agent, operator). See
	// migrate_consts_v112_user_models.go.
	{version: 112, name: "user_models", sql: migrationUserModels},

	// v113: plain status indexes on assignments + pipeline_runs so the
	// /metrics domain gauges (W10) count by status off an index instead
	// of scanning. See migrate_consts_v113_metrics_indexes.go.
	{version: 113, name: "metrics_status_indexes", sql: migrationMetricsStatusIndexes},

	// v114: definition_hash on pipeline_runs — the content hash of the
	// pipeline as of run start, so boot-time resume can refuse to
	// replay persisted step outputs against an edited definition even
	// when every step id survived the edit. See
	// migrate_consts_v114_pipeline_runs_definition_hash.go.
	{version: 114, name: "pipeline_runs_definition_hash", sql: migrationPipelineRunsDefinitionHash},
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

// migrationAddCaptainChats: Deprecated schema for the deprecated Captain feature.
// Retained for backward compat. See internal/api/captain.go for deprecation details.
