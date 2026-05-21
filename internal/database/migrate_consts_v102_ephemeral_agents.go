package database

// migrationEphemeralAgents (v102) adds the ephemeral-agent lifecycle
// surface introduced in PRD §6 F5 (PR-D). Five net-new columns on
// agents + one on crews, all additive so legacy inserts continue to
// work unchanged.
//
//   - agents.ephemeral INTEGER NOT NULL DEFAULT 0
//     Boolean (0/1) flag. 0 = permanent (legacy default — the agent
//     lives until soft-deleted by an admin); 1 = ephemeral
//     (contractor-style; the row enters "ghost" state when its TTL
//     elapses or its hire goal completes).
//
//   - agents.expires_at TEXT
//     RFC3339 deadline. NULL on permanent agents AND on ephemeral
//     agents that haven't been ticked yet. The EphemeralExpiry
//     sweeper compares this against now() to flip expired_at; the
//     rehire endpoint resets it to now() + ttl.
//
//   - agents.expired_at TEXT
//     The "ghost" mark. NULL = live (or pending TTL expiry); NOT
//     NULL = expired (container recycled, DB row preserved for
//     audit and rehire). The list query orders live rows first and
//     ghosts last by COALESCE(expired_at, created_at) DESC.
//
//   - agents.parent_lead_id TEXT
//     Soft FK to agents(id) for lead-driven hire: when a LEAD calls
//     the sidecar /spawn endpoint, the resulting ephemeral row
//     points back at the lead that authored it. NULL on
//     human-triggered hires (the audit log records the user_id).
//     Soft FK (no REFERENCES) so deleting the parent LEAD doesn't
//     cascade-delete the audit trail of the ephemerals it spawned.
//
//   - agents.hire_reason TEXT
//     Append-only free text. First write is the reason from the
//     /api/v1/agents/hire body; each rehire appends a new line with
//     its own reason + timestamp so operators can see the full
//     contractor history on the agent's audit tab.
//
//   - crews.max_ephemeral_agents INTEGER NOT NULL DEFAULT 10
//     Per-crew quota. The hire endpoint counts live ephemerals
//     (ephemeral=1 AND expired_at IS NULL AND deleted_at IS NULL)
//     and rejects with 429 when the quota is hit. Ghost rows do
//     NOT count (the whole point of ghost state is to preserve
//     history without consuming quota). The default of 10 matches
//     the seed crew workload expected by PRD §6 F5 — operators can
//     raise via PATCH /api/v1/crews/{id} once the quota proves
//     too tight in practice.
//
// CHECK constraint on ephemeral is column-level; SQLite recreate
// dance is unnecessary for net-new ADD COLUMN paths. The companion
// indexes (idx_agent_expires_at, idx_agent_parent_lead) speed the
// sweeper scan (WHERE expires_at < ? AND expired_at IS NULL) and
// the LEAD-driven cleanup lookup respectively.
const migrationEphemeralAgents = `
ALTER TABLE agents ADD COLUMN ephemeral INTEGER NOT NULL DEFAULT 0
    CHECK(ephemeral IN (0, 1));

ALTER TABLE agents ADD COLUMN expires_at TEXT;
ALTER TABLE agents ADD COLUMN expired_at TEXT;
ALTER TABLE agents ADD COLUMN parent_lead_id TEXT;
ALTER TABLE agents ADD COLUMN hire_reason TEXT;

CREATE INDEX IF NOT EXISTS idx_agent_expires_at
    ON agents(expires_at)
    WHERE expires_at IS NOT NULL AND expired_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_agent_parent_lead
    ON agents(parent_lead_id)
    WHERE parent_lead_id IS NOT NULL;

ALTER TABLE crews ADD COLUMN max_ephemeral_agents INTEGER NOT NULL DEFAULT 10
    CHECK(max_ephemeral_agents >= 0);
`
