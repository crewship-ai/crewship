-- Every run attributable to its issue (#2256, PRD-ISSUES-AND-ROUTINES-2026
-- work package A2).
--
-- assignments — the run table — had no column naming the mission (issue)
-- whose work it is. The only issue<->run link on the whole schema was
-- mission_comment_mentions.assignment_id, and that table records only
-- MENTION dispatches: a mission task's own run (mission_tasks.assignment_id)
-- and a lead-planning run (mission_tasks_planning.go) name no mention at
-- all, so issue_handler_runs.go's ListRuns could only find them by joining
-- through mission_tasks — a join a mention dispatch never populates. "Show
-- me every run for this issue" was unanswerable for exactly the runs that
-- happen when an issue comment @mentions an agent.
--
-- Nullable, no default: an existing row said nothing about which mission
-- caused it, and a reader must not invent one. The companion backfill
-- migration (20260901180224) recovers what mission_comment_mentions can
-- prove; everything else stays NULL, which is the honest answer for a row
-- nothing else names.
--
-- ON DELETE SET NULL — deliberately neither of this table's two existing
-- shapes:
--
--   * workspace_id is ON DELETE CASCADE, but that is right only because
--     EVERY mission is reachable from exactly one workspace and a workspace
--     delete is meant to take everything under it. A mission delete is not
--     that: crews_query.go's crew-scoped bulk delete, issue_handler_update.go
--     (BACKLOG/CANCELLED), mission_handler_mutate.go (PLANNING/CANCELLED)
--     and missions_internal.go's admin delete all hard-delete individual
--     `missions` rows while the workspace, and every other issue in it,
--     lives on. CASCADE here would delete the assignment — the run itself —
--     the moment an operator cleaned up a stale issue, which is exactly the
--     run history this package exists to keep answerable.
--
--   * chat_id/assigned_by_id/assigned_to_id carry no ON DELETE clause
--     (NO ACTION) because their parents (chats, agents) are not hard-deleted
--     out from under a live assignment in the same way. missions ARE — see
--     the four call sites above — so NO ACTION would make every one of them
--     start refusing as soon as the issue had ever dispatched a single run,
--     which in practice is every issue that ever reached IN_PROGRESS.
--
--   SET NULL is the choice this schema already made for the one existing
--   issue<->run edge, mission_comment_mentions.assignment_id -> assignments
--   (20260806172731): the child survives, the link degrades to "cause
--   unknown" instead of taking the evidence down with the deleted parent.
ALTER TABLE assignments
    ADD COLUMN mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL;

-- "Every run for this issue, newest first" (issue_handler_runs.go's
-- ListRuns) is the one query this index exists for. Partial, like
-- idx_assignment_chain_origin and idx_assignment_parent_run beside it: a row
-- with no mission_id is never this query's answer, and most existing rows
-- will have none until the companion backfill and the write paths below
-- have run.
CREATE INDEX IF NOT EXISTS idx_assignment_mission_created
    ON assignments (mission_id, created_at DESC)
    WHERE mission_id IS NOT NULL;
