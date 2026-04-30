package database

// SQL constants for migrations v42–v45 (Phase-2 features, FK + perf
// indexes, crew journal). See migrate.go for the Migrate driver and
// the registry slice.

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
