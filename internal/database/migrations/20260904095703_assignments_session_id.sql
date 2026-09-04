-- assignments.session_id (PRD-ISSUES-AND-ROUTINES-2026 §9.4, work package
-- B1's slice of it — the column only).
--
-- Links a run to the issue_agent_sessions row it belongs to. Nullable, no
-- default: a run dispatched before this migration, or one whose target
-- issue has no session concept engaged for it (a root /assign with no
-- mention involved, for instance — B1 wires resolve-or-create only into the
-- mention dispatch path, DispatchMention), correctly says nothing about a
-- session it does not have.
--
-- ON DELETE SET NULL, same reasoning §9.4/F55 gives assignments.mission_id
-- and A10's owner/delegate columns: a session row disappearing (its issue or
-- agent hard-deleted, cascading through issue_agent_sessions) must degrade
-- the run's session pointer to "unknown", not take the run history down
-- with it.
--
-- What THIS migration deliberately does not add: the partial unique index
-- (`idx_assignments_one_active_per_session`, §9.4) that makes session_id an
-- exclusivity key is B3's job, not B1's — B1 only claims "seq is monotonic
-- under concurrent writers" and "a mention reuses an existing session",
-- neither of which needs a run-level exclusivity guarantee. Adding the index
-- now, ahead of the transactional resolve-or-create + fan-out-guard rewrite
-- B3 requires, would ship a constraint with no write path proven safe
-- against the TOCTOU window §9.4 names.
ALTER TABLE assignments
    ADD COLUMN session_id TEXT REFERENCES issue_agent_sessions(id) ON DELETE SET NULL;

-- "every run this session has spawned" — the session-side read.
CREATE INDEX IF NOT EXISTS idx_assignments_session
    ON assignments(session_id)
    WHERE session_id IS NOT NULL;
