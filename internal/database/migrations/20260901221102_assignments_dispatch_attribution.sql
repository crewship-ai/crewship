-- dispatchByID (internal/api/assignments_dispatch_pump.go) rebuilds a
-- requeued assignment's dispatch door purely from the `assignments` row —
-- it is the completion-path pump's only input once a row has gone QUEUED
-- (chatbridge.AgentRunLock cross-surface requeue, #2269 follow-up). Three
-- things it needs were never on the row:
--
--   * mission_id     — dispatchByID read this off `group_id`, but group_id
--                       is NOT always a mission id: Create's /assign door
--                       (assignments_run.go) sets group_id = chat_id, since
--                       that door has no mission. A requeued /assign row
--                       re-dispatched with a chat id standing in for
--                       MissionID would attach its re-run to the wrong
--                       journal mission scope (or an unrelated missions
--                       row, if the ids ever collided). DispatchMention and
--                       the mission engine's own two INSERTs already set
--                       group_id = mission_id, which is why this went
--                       unnoticed — every row that ever reached dispatchByID
--                       pre-#2269 came from one of those two, never Create.
--   * author_agent_id / created_by_user_id — creator attribution (v129,
--                       #810). Set on the in-memory `body` at every dispatch
--                       door, but never persisted, so it existed only for
--                       the ORIGINAL dispatch, not a requeued re-dispatch.
--   * lead_planning   — DispatchAssignment skips the crew-budget CAS for a
--                       mission lead's own planning turn (a stuck lead
--                       deadlocks its whole mission on its own
--                       sub-assignments' budget) and runs it with the LEAD
--                       role + sidecar. dispatchByID hard-codes
--                       LeadPlanning:false on every pumped row — correct
--                       for a normal worker row, wrong for a lead-planning
--                       row that lost the lock and got requeued: the
--                       re-dispatch would run the lead as a plain AGENT,
--                       silently dropping its sidecar mid-mission.
--
-- All four are nullable/DEFAULT-false rather than backfilled: every
-- existing row was dispatched exactly once already (nothing pre-#2269 could
-- reach dispatchByID with a row that needed rebuilding), so there is no
-- historical value to recover — a NULL/0 on an old row is the truthful
-- "this was never re-dispatched from cold storage" answer, not a gap.
ALTER TABLE assignments ADD COLUMN mission_id TEXT;
ALTER TABLE assignments ADD COLUMN author_agent_id TEXT;
ALTER TABLE assignments ADD COLUMN created_by_user_id TEXT;
ALTER TABLE assignments ADD COLUMN lead_planning INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_assignments_mission ON assignments(mission_id);
