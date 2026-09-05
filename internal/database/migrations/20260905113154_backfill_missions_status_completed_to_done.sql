-- B13 (#2370, PRD-ISSUES-AND-ROUTINES-2026 §3.1): DONE is now the sole
-- terminal "approved out of review" word on missions.status. COMPLETED —
-- written only by the mission PATCH handler's REVIEW→terminal transition
-- (internal/api/mission_handler_mutate.go) for mission_type='orchestration'
-- rows, alongside DONE written by the same action on mission_type='issue'
-- rows (internal/api/issue_handler_workflow.go's Review handler) — is
-- retired. See internal/statuses/transitions.go's ValidMissionTransitions.
--
-- No schema change: missions.status carries no CHECK constraint, so this
-- migration is data-only, in its own file per the repo's schema/backfill
-- split convention (e.g. 20260901180223/...224, 20260902080721/...722) —
-- a data-only UPDATE has no idempotent rebuild form to fold it into.
--
-- Scope: missions.status ONLY. assignments.status and pipeline_runs.status
-- keep 'COMPLETED'/'completed' untouched — see the CAS pin tests in
-- internal/api and internal/pipeline.
--
-- Read compatibility: the mission PATCH handler still accepts a legacy
-- "COMPLETED" input and normalizes it to DONE before writing, so an
-- old client is not broken by this backfill — see mission_handler_mutate.go.
UPDATE missions
SET status = 'DONE'
WHERE status = 'COMPLETED';
