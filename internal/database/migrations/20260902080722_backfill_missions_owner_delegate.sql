-- Backfill for 20260902080721_missions_owner_delegate.sql, which added the
-- two columns with no backfill.
--
-- Separate migration file, not folded into the ADD COLUMN above: SQLite's
-- ALTER TABLE ADD COLUMN has no idempotent form (no IF NOT EXISTS), so a
-- migration that both adds a column and re-runs the backfill can never be
-- safely re-applied. Splitting them is what lets an upgrade re-run just the
-- UPDATE — same shape as 20260901180223/20260901180224
-- (assignments_mission_id / backfill_assignments_mission_id) and
-- 20260822203500/20260823190000 (add_onboarding_skipped_at /
-- backfill_onboarding_skipped_at).
--
-- assignee_type/assignee_id is the only place the owner or delegate of an
-- existing row is recorded, and it is exactly this pair each write path is
-- being changed to stop trusting as the single source: assignee_type='user'
-- means the row's owner is assignee_id; assignee_type='agent' means the
-- row's delegate is assignee_id. Both guarded by "column IS NULL" so a
-- re-run (or a row a later write path already populated directly) is never
-- overwritten.
UPDATE missions
SET owner_user_id = assignee_id
WHERE assignee_type = 'user'
  AND assignee_id IS NOT NULL
  AND assignee_id != ''
  AND owner_user_id IS NULL;

UPDATE missions
SET delegate_agent_id = assignee_id
WHERE assignee_type = 'agent'
  AND assignee_id IS NOT NULL
  AND assignee_id != ''
  AND delegate_agent_id IS NULL;
