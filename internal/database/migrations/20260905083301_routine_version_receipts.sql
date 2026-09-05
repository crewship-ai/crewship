-- §9.8's one addition to the decision-receipt columns that already exist on
-- approvals_queue and pipeline_waitpoints (decided_by[_user_id], decided_at,
-- payload/decision_payload): `routine_version`, so "was it the SAME version
-- that then ran?" is answerable without a seventh table
-- (PRD-ISSUES-AND-ROUTINES-2026 §9.8, work package B10, #2364).
--
-- Nullable INTEGER on both — most rows on both tables have nothing to do
-- with a routine (a credential approval, a hiring approval, an ordinary
-- pipeline wait-for-approval step) and stay NULL. It is stamped only where
-- the decision concerns a routine's authored definition:
--   - approvals_queue: internal/api/internal_autonomy_gate.go's
--     writeAutonomyHold, when the autonomy hold's target is a routine
--     schedule (internal/api/internal_routines.go's CreateSchedule adapter,
--     the #1768 autonomy gate over agent-authored routine schedules).
--   - pipeline_waitpoints: internal/pipeline/waitpoints.go's CreateApproval,
--     resolved from the run's own pipeline_runs.pipeline_version (or the
--     pipeline's head_version when the run pinned none) at the moment the
--     waitpoint is raised — the version that was actually running when a
--     human was asked to decide.
--
-- Plain ADD COLUMN, no rebuild: neither column carries a CHECK and both
-- tables have no dependent that a DROP/rename would cascade against.
ALTER TABLE approvals_queue
    ADD COLUMN routine_version INTEGER;

ALTER TABLE pipeline_waitpoints
    ADD COLUMN routine_version INTEGER;
