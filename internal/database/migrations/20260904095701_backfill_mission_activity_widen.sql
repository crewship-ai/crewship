-- Backfill for 20260904095700_mission_activity_widen.sql, which added
-- workspace_id and seq with no backfill.
--
-- Separate migration file, not folded into the rebuild above: SQLite's
-- ALTER TABLE / table-rebuild has no idempotent form for a data-only UPDATE,
-- so a migration that both rebuilds the table and re-runs the backfill can
-- never be safely re-applied. Same shape as 20260901180223/20260901180224
-- (assignments_mission_id / backfill_assignments_mission_id) and
-- 20260902080721/20260902080722 (missions_owner_delegate / its backfill).
--
-- ── workspace_id ────────────────────────────────────────────────────────
--
-- §9.1: "mission_activity.workspace_id backfills by joining missions."
-- Every row names a mission_id (NOT NULL, FK); the join recovers workspace_id
-- for every row whose mission still exists. A row whose mission was hard-
-- deleted between the original write and this migration cascades away with
-- it (mission_id ON DELETE CASCADE), so there is no orphan case to handle
-- here — anything this UPDATE cannot resolve is legacy-shaped rows written
-- against a chat id rather than a real missions.id (see issue_events.go's
-- own comment on that legacy path), which correctly keep workspace_id NULL:
-- inventing a workspace for a row that never named a real mission would be
-- worse than leaving the honest gap.
UPDATE mission_activity
SET workspace_id = (
    SELECT m.workspace_id FROM missions m WHERE m.id = mission_activity.mission_id
)
WHERE workspace_id IS NULL
  AND mission_id IN (SELECT id FROM missions);

-- ── seq ─────────────────────────────────────────────────────────────────
--
-- Not required by §9.1's own words (it names only the workspace_id backfill
-- explicitly), but leaving every pre-existing row's seq NULL would mean the
-- FIRST session ever opened on any issue with history sees an empty delta
-- forever — every existing comment/status-change would be permanently
-- "before this cursor" rather than "unread". Backfilling gives existing
-- issues the same ordered history new ones get.
--
-- ROW_NUMBER() OVER (PARTITION BY mission_id ORDER BY created_at, id) is the
-- least-surprising order available: `created_at` is the only ordering
-- signal these rows have ever carried (no seq existed until this release),
-- and `id` (this table's CUIDs are time-sortable) breaks same-instant ties
-- deterministically rather than by SQLite's unspecified row order. Written
-- as an UPDATE ... FROM-shaped derived table, not a scalar correlated
-- subquery per row, because a per-row COALESCE(MAX(seq),0)+1 correlated
-- subquery run mission-by-mission during a bulk backfill is O(n^2) on a
-- mission with many rows; the windowed derived table computes every
-- mission's numbering in one pass.
UPDATE mission_activity
SET seq = (
    SELECT numbered.rn
    FROM (
        SELECT id, ROW_NUMBER() OVER (
            PARTITION BY mission_id ORDER BY created_at, id
        ) AS rn
        FROM mission_activity
    ) AS numbered
    WHERE numbered.id = mission_activity.id
)
WHERE seq IS NULL;
