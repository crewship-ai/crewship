-- Owner and delegate as typed columns on missions (PRD-ISSUES-AND-ROUTINES-2026
-- work package A10; invariant I5 "delegating to an agent never changes the
-- human owner"; rev-1 dev1 observation 11; F62).
--
-- missions carries a polymorphic assignee_type/assignee_id, and the one place
-- that delegates an issue to an agent (AssignmentHandler.Create,
-- internal/api/assignments_run.go) overwrote that single slot with the
-- agent, so the UI rendered the agent in the owner slot and the responsible
-- human disappeared. There was no schema that could enforce I5 because there
-- was only one slot to hold either kind of actor. Two independent, nullable
-- FK columns fix that: a human owner and an agent delegate can both be set,
-- neither write path can clobber the other's column, and Start (F62) gets a
-- typed FK to check instead of a bare existence test on a column that could
-- name either a user or an agent.
--
-- Nullable, no default, same as assignee_type/assignee_id themselves: an
-- issue with nobody assigned says nothing about either slot. The companion
-- backfill migration (20260902080722) recovers what the legacy pair can
-- prove for existing rows.
--
-- ON DELETE SET NULL on both (F55, the same choice A2 made for
-- assignments.mission_id): deleting a user or an agent must not delete the
-- issue that names them. missions.lead_agent_id (v8) has no ON DELETE clause
-- at all, but that column is NOT NULL — every mission requires a lead agent
-- that exists at insert time, so a hard delete of that agent was never a
-- case the schema had to answer. owner_user_id and delegate_agent_id are
-- both optional and populated well after the row exists, so the parent
-- (user, agent) predictably outlives or is outlived by the mission; SET NULL
-- degrades the reference to "nobody" instead of taking the issue down with
-- the account, matching REFERENCES users(id) ON DELETE SET NULL /
-- REFERENCES agents(id) ON DELETE SET NULL used elsewhere on this schema
-- (e.g. migrate_consts_v78_pipelines.go's author_user_id/author_agent_id).
ALTER TABLE missions ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE missions ADD COLUMN delegate_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;

-- Partial indexes, same shape as idx_mission_assignee beside them: a row
-- with no owner/delegate is never the answer to "issues owned/delegated to
-- X", so there is nothing to index there.
CREATE INDEX IF NOT EXISTS idx_mission_owner_user ON missions(owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mission_delegate_agent ON missions(delegate_agent_id) WHERE delegate_agent_id IS NOT NULL;
