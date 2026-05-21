package database

// migrationKeeperPhase2 (v102) widens the keeper_requests.request_type
// column to a CHECK-constrained enum and lands the schema surface F4
// evaluators consume.
//
// PRD §6 F4 (PR-C) introduces four NEW request types on top of the
// existing 'access' (v9) + 'execute' (v10):
//
//	skill_review        F4.1 — agent-proposed or routine-driven skill audit
//	behavior            F4.2 — sampled post-tool-call behavior monitor
//	memory_health       F4.3 — daily AGENT.md / CREW.md hygiene sweep
//	negative_learning   F4.4 — failure-driven lessons.md writer
//
// Today keeper_requests.request_type is a free-form TEXT column with
// DEFAULT 'access'. Adding a CHECK constraint after the fact requires
// the SQLite recreate dance because SQLite does not support
// ALTER TABLE ADD CONSTRAINT. The recreate also lets us add the FK on
// requesting_agent_id (carried from v9) verbatim — the table layout
// is byte-equivalent to the v9 + v10 + v11 cumulative shape, with the
// single change being the typed enum on request_type.
//
// skill_invocations is the new audit log F4.1 reads at evaluation time
// to decide whether a skill is actively in use. One row per
// skill→agent invocation. Indexed on (skill_id, invoked_at DESC) so
// the evaluator's "how recently was this skill called and by whom?"
// hot path is a single bounded range scan.
//
// skills lifecycle columns add the per-skill state machine the F4.1
// evaluator + curator UI render:
//
//	lifecycle_state  active → stale → archived → deprecated
//	last_used_at     timestamp of most recent skill_invocations row
//	usage_count      monotonic counter (cheaper than COUNT(*) at read)
//	error_count      invocations that returned a non-zero error code
//
// The assignment-trumps-timer rule (assigned skills never auto-stale)
// lives in the evaluator code path; the column-level CHECK only
// constrains valid state values. Defaults preserve back-compat: every
// pre-v102 skill lands as 'active' with NULL last_used_at and zero
// counters, matching the implicit semantics callers already assume.
//
// Backwards-compat invariant: every existing keeper_requests row keeps
// its request_type ('access' or 'execute' — both pre-existing). The
// recreate preserves every column value byte-for-byte, the new CHECK
// admits the existing values, and INSERT statements that omit
// request_type continue to land 'access' via the default.
const migrationKeeperPhase2 = `
-- keeper_requests: rebuild to add a CHECK on request_type that admits
-- the four new F4 kinds. Bash-recreate-dance is required because
-- SQLite has no ALTER TABLE ADD CONSTRAINT.
ALTER TABLE keeper_requests RENAME TO keeper_requests_pre_v102;

-- requesting_agent_id, requesting_crew_id, credential_id all relax
-- to NULLable as part of the v100 recreate. The original v9 schema
-- required them because every row was a credential-access request;
-- F4 introduces request types (skill_review / behavior /
-- memory_health / negative_learning) where the agent + crew + cred
-- triple isn't meaningful (a daily routine sweep doesn't act on
-- behalf of a single agent; a memory-health check doesn't touch
-- a credential). Existing access/execute callers always populate
-- these so the relaxation is invisible to them.
CREATE TABLE keeper_requests (
    id TEXT PRIMARY KEY,
    requesting_agent_id TEXT REFERENCES agents(id),
    requesting_crew_id TEXT,
    credential_id TEXT REFERENCES credentials(id),
    task_id TEXT,
    intent TEXT NOT NULL,
    decision TEXT,
    reason TEXT,
    risk_score INTEGER,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    decided_at TEXT,
    request_type TEXT NOT NULL DEFAULT 'access'
        CHECK(request_type IN ('access','execute','skill_review','behavior','memory_health','negative_learning')),
    command TEXT,
    exit_code INTEGER,
    ollama_prompt TEXT,
    ollama_raw_response TEXT
);

INSERT INTO keeper_requests (
    id, requesting_agent_id, requesting_crew_id, credential_id,
    task_id, intent, decision, reason, risk_score, created_at,
    decided_at, request_type, command, exit_code,
    ollama_prompt, ollama_raw_response
)
SELECT id, requesting_agent_id, requesting_crew_id, credential_id,
       task_id, intent, decision, reason, risk_score, created_at,
       decided_at, request_type, command, exit_code,
       ollama_prompt, ollama_raw_response
FROM keeper_requests_pre_v102;

DROP TABLE keeper_requests_pre_v102;

CREATE INDEX IF NOT EXISTS idx_keeper_req_agent ON keeper_requests(requesting_agent_id);
CREATE INDEX IF NOT EXISTS idx_keeper_req_crew ON keeper_requests(requesting_crew_id);
CREATE INDEX IF NOT EXISTS idx_keeper_req_cred ON keeper_requests(credential_id);
CREATE INDEX IF NOT EXISTS idx_keeper_req_decision ON keeper_requests(decision);
CREATE INDEX IF NOT EXISTS idx_keeper_req_created ON keeper_requests(created_at);
-- F4 routing index: keeper UI groups requests by type. Partial index
-- keeps the access/execute hot path uncluttered.
CREATE INDEX IF NOT EXISTS idx_keeper_req_type_created
    ON keeper_requests(request_type, created_at DESC);

-- skills lifecycle columns. Net-new; defaults preserve pre-v102
-- behaviour exactly. CHECK on lifecycle_state is column-level (allowed
-- on ALTER ADD COLUMN in SQLite when the column has a DEFAULT).
ALTER TABLE skills ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active'
    CHECK(lifecycle_state IN ('active','stale','archived','deprecated'));
ALTER TABLE skills ADD COLUMN last_used_at TEXT;
ALTER TABLE skills ADD COLUMN usage_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE skills ADD COLUMN error_count INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_skill_lifecycle ON skills(lifecycle_state);
CREATE INDEX IF NOT EXISTS idx_skill_last_used
    ON skills(last_used_at DESC) WHERE last_used_at IS NOT NULL;

-- skill_invocations: per-call audit log feeding F4.1's "is this skill
-- actually in use?" decision + the lifecycle column denormalisation.
-- exit_code mirrors the keeper.execute convention (0 = success, non-
-- zero = error); error_count on skills is bumped when exit_code != 0.
-- workspace_id is denormalised so the cleanup sweep can scope by
-- workspace without a join when skill rows are workspace-shared.
CREATE TABLE IF NOT EXISTS skill_invocations (
    id TEXT PRIMARY KEY,
    skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    invoked_at TEXT NOT NULL DEFAULT (datetime('now')),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    exit_code INTEGER NOT NULL DEFAULT 0,
    -- payload_json carries optional tool-call args / outputs the F4.1
    -- evaluator may inspect (e.g. "skill was called but with empty
    -- args" → ESCALATE). Bounded by the inserter, not the schema.
    payload_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_skill_inv_skill_time
    ON skill_invocations(skill_id, invoked_at DESC);
CREATE INDEX IF NOT EXISTS idx_skill_inv_agent_time
    ON skill_invocations(agent_id, invoked_at DESC);
CREATE INDEX IF NOT EXISTS idx_skill_inv_ws_time
    ON skill_invocations(workspace_id, invoked_at DESC);
`
