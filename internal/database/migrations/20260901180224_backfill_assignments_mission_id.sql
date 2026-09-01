-- Backfill for 20260901180223_assignments_mission_id.sql, which added the
-- column with no backfill.
--
-- mission_comment_mentions has named both sides (mission_id, assignment_id)
-- of every dispatched mention since 20260806172731 — a mention dispatched
-- before the mission_id column existed on assignments is exactly the case
-- this recovers, and it is the only place left that can recover it: the
-- mention row is deleted along with its comment (ON DELETE CASCADE), so
-- once this migration runs once, any mention row it missed is gone for good.
--
-- Expected to touch ~0 rows on a real clone: mention dispatch is new enough,
-- and mission_comment_mentions holds 0 rows on the live dev clone at the
-- time of writing (#2256's own defect report). The UPDATE is written for the
-- non-zero case anyway, because "backfill that only runs against fixtures"
-- is not a backfill.
--
-- Separate migration file, not folded into the ADD COLUMN above: SQLite's
-- ALTER TABLE ADD COLUMN has no idempotent form (no IF NOT EXISTS), so a
-- migration that both adds the column and re-runs the backfill can never be
-- safely re-applied. Splitting them is what lets an upgrade re-run just the
-- UPDATE — same shape as 20260822203500 / 20260823190000
-- (add_onboarding_skipped_at / backfill_onboarding_skipped_at).
UPDATE assignments
SET mission_id = (
    SELECT mcm.mission_id
    FROM mission_comment_mentions mcm
    WHERE mcm.assignment_id = assignments.id
    LIMIT 1
)
WHERE mission_id IS NULL
  AND id IN (SELECT assignment_id FROM mission_comment_mentions WHERE assignment_id IS NOT NULL);
