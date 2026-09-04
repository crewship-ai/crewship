-- One active turn per session — the partial unique index (PRD-ISSUES-AND-
-- ROUTINES-2026 §9.4, invariant I2, work package B3 — #2339).
--
-- Two runs of the same issue_agent_sessions row can never both be
-- PENDING/QUEUED/RUNNING at once. Before this migration nothing enforced
-- that: assignments.session_id (20260904095703) exists and is set by
-- DispatchMention, but no constraint backs it, so a second concurrent
-- mention of a busy agent would happily insert a second live run for the
-- same session — the exact TOCTOU §9.4 names by pointing at
-- insertCappedAssignment's INSERT having "no session_id in its struct or
-- its INSERT" before B1 (that half is now fixed), and at the index
-- "guard[ing] nothing until insertCappedAssignment sets the column" (this
-- migration is the other half).
--
-- The insert-path rewrite that depends on this index — resolving-or-
-- creating the session and re-proving the fan-out guard inside ONE
-- transaction, and turning the loser of this constraint into a queued
-- delivery instead of a raw 500 — lives in internal/api/delegation_limits.go
-- and internal/api/issue_mentions.go (DispatchMention), not here. This
-- migration only adds the constraint those two files now assume exists.
--
-- Partial, matching idx_agents_one_lead_per_crew's shape
-- (migrate_consts_v110_one_lead_per_crew.go): only PENDING/QUEUED/RUNNING
-- rows with a non-NULL session_id are constrained, so terminal runs
-- (CANCELLED/COMPLETED/FAILED) and every pre-B1 row (session_id NULL — a
-- mission task, a root /assign, anything dispatched before B1) are exempt.
-- NULLs never collide in a unique index, so any number of NULL-session
-- rows coexist exactly as before.
--
-- Remediation-first, same reasoning as v110: session_id has only ever been
-- written by DispatchMention (since 20260904095703, days before this
-- migration), so a live violation is very unlikely — but CREATE UNIQUE
-- INDEX aborts the whole migration if one exists, so the migration must not
-- assume that. Keep the earliest (lowest rowid) non-terminal run per
-- session and move any later duplicate straight to CANCELLED — the
-- "demote the extra, don't fail the migration" shape v110 uses for a
-- LEAD-per-crew violation, with CANCELLED standing in for "demoted" because
-- an assignment's terminal-but-safe state is cancellation, not a role
-- change. Cancelling a row that turns out to still be genuinely running is
-- not a silent loss: A1's terminal-state guards refuse any further write to
-- a CANCELLED row, and the run's own completion path (finishAssignment)
-- already handles a late callback on an already-terminal row as a no-op.
UPDATE assignments SET status = 'CANCELLED'
WHERE session_id IS NOT NULL
  AND status IN ('PENDING','QUEUED','RUNNING')
  AND rowid NOT IN (
    SELECT MIN(rowid) FROM assignments
    WHERE session_id IS NOT NULL AND status IN ('PENDING','QUEUED','RUNNING')
    GROUP BY session_id
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_assignments_one_active_per_session
    ON assignments(session_id)
    WHERE status IN ('PENDING','QUEUED','RUNNING') AND session_id IS NOT NULL;
