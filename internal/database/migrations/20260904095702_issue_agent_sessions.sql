-- issue_agent_sessions (PRD-ISSUES-AND-ROUTINES-2026 §9.2, work package B1).
--
-- The durable, evolving cursor an agent holds on one issue — irreducible
-- because nothing else on the schema has this shape. §9.2 checked the two
-- closest candidates and ruled both out: mission_comment_mentions is keyed
-- UNIQUE(comment_id, agent_id) — many rows per (mission, agent) pair, the
-- wrong cardinality to hold ONE evolving cursor; mission_tasks.handoff_context
-- is one column, overwritten, scoped to a task rather than to an
-- (issue, agent) relationship that outlives any single task or run.
--
-- Thinner than rev 1: opened_by_user_id and opened_reason are deliberately
-- absent (§9.2) — logged in the mission_activity event payload that opens the
-- session instead of carried as columns nothing queries.
--
-- ── Shape ───────────────────────────────────────────────────────────────
--
-- UNIQUE(mission_id, agent_id): the B1 accept line's own words — "a mention
-- reuses an existing session rather than creating a second". This is that
-- guarantee at the only layer that holds under concurrent writers; the
-- resolve-or-create write path (issue_mentions.go) is an UPSERT against this
-- exact constraint, not a SELECT-then-INSERT.
--
-- state: the §10.1 session state machine's vocabulary. B1 ships the column,
-- the CHECK, and 'pending' as the only state a session is created in — the
-- transitions themselves (claim CAS into 'active', the lease sweep into
-- 'error', the 14-day idle sweep into 'stale') are B2/B4 work: the CHECK
-- exists now so nothing downstream needs a second migration to add a state
-- the model already names.
--
-- last_consumed_seq: the cursor into mission_activity.seq (§9.1) — "every
-- row above this is unread for this session" (§11.1 item 4). Defaults to 0,
-- not NULL: a fresh session has read nothing, and 0 compares correctly
-- against `seq > last_consumed_seq` without every reader needing a NULL
-- check first.
--
-- active_run_id: nullable, no FK. assignments rows are frequently deleted-
-- and-recreated-shaped in tests and can outlive or be outlived by the
-- session that spawned them in ways a hard FK would fight; a stale id here
-- costs a lookup miss, not a leak, the same trade-off assignments.
-- parent_run_id already makes.
--
-- agent_version: stamped from agent_config_history at session creation
-- (§11.6) — "why did it behave differently on Thursday" becomes answerable
-- because the session pins which prompt version it woke under. NULL when the
-- agent has no config-history row yet (never edited since creation).
--
-- ── Cascades (F55) ──────────────────────────────────────────────────────
--
-- workspace_id: CASCADE — every new table's own copy of the rule, never
-- relying on the mission chain (I7).
-- mission_id: CASCADE — a session is meaningless once its issue is gone;
-- matches mission_comment_mentions' choice for the identical question.
-- agent_id: CASCADE — matches mission_comment_mentions.agent_id; an agent
-- hard-delete takes its sessions with it rather than leaving a session
-- pointed at nothing.

CREATE TABLE IF NOT EXISTS issue_agent_sessions (
    id               TEXT NOT NULL PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mission_id       TEXT NOT NULL REFERENCES missions(id)   ON DELETE CASCADE,
    agent_id         TEXT NOT NULL REFERENCES agents(id)     ON DELETE CASCADE,

    state             TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','active','awaiting_input','idle','error','stale','closed')),
    last_consumed_seq INTEGER NOT NULL DEFAULT 0,
    active_run_id     TEXT,
    agent_version     INTEGER,

    last_activity_at TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    UNIQUE (mission_id, agent_id)
);

-- "every session on this issue" — the issue-side read (a future session
-- panel on the issue detail view).
CREATE INDEX IF NOT EXISTS idx_issue_agent_sessions_mission ON issue_agent_sessions(mission_id);

-- "every session this agent holds" — the agent-side read, and the query the
-- 14-day idle sweep (B4) will run.
CREATE INDEX IF NOT EXISTS idx_issue_agent_sessions_agent ON issue_agent_sessions(agent_id);

CREATE INDEX IF NOT EXISTS idx_issue_agent_sessions_workspace ON issue_agent_sessions(workspace_id);

-- Consistency triggers, copied from the mention tables' shape
-- (trg_mission_comment_mentions_consistency_ins/upd,
-- 20260807094500_mention_workspace_consistency.sql) rather than trusting the
-- three FKs above to agree with each other: a mission and an agent can both
-- exist and both be real while belonging to DIFFERENT workspaces, and
-- nothing about a bare FK reference catches that. §16.1's tenancy rule
-- applies to this table exactly as it does to the mention tables.
CREATE TRIGGER IF NOT EXISTS trg_issue_agent_sessions_consistency_ins
BEFORE INSERT ON issue_agent_sessions
BEGIN
    SELECT RAISE(ABORT, 'session mission must belong to the session workspace')
    WHERE (SELECT workspace_id FROM missions WHERE id = NEW.mission_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'session agent must belong to the session workspace')
    WHERE (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_issue_agent_sessions_consistency_upd
BEFORE UPDATE ON issue_agent_sessions
BEGIN
    SELECT RAISE(ABORT, 'session mission must belong to the session workspace')
    WHERE (SELECT workspace_id FROM missions WHERE id = NEW.mission_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'session agent must belong to the session workspace')
    WHERE (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;
