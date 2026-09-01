-- Stop actually stops (Tier 1, cooperative) — the missing signal.
--
-- IssueHandler.Stop (internal/api/issue_handler_workflow.go) has only ever
-- written mission_tasks and missions to CANCELLED. It never touched
-- assignments, and nothing cancels the context the dispatch goroutine runs
-- on (context.Background(), by design — see assignments_run.go) or kills
-- the sub-agent's exec. There is today no column anywhere that records "an
-- operator asked this run to stop" — a stopped issue and a merely-slow one
-- are indistinguishable from the assignment row alone.
--
-- cancel_requested_at is that signal. Stop stamps it on the issue's live
-- (PENDING/RUNNING) assignment rows in the same transaction as the existing
-- mission_tasks/missions writes. Nothing here claims the run actually
-- stops running — there is no kill primitive for a shared crew container
-- (internal/provider/container.go has Exec/ExecInspect, no Kill) — this is
-- the cooperative half: the runner checks the flag before starting further
-- work (assignments_run.go's runAssignment, at the top, before any exec is
-- spent) and, if the run already finished by the time anyone looks, records
-- CANCELLED instead of resurrecting it as COMPLETED.
--
-- Nullable, no default: an existing row means "nobody asked", which is what
-- every row before this migration is.
ALTER TABLE assignments ADD COLUMN cancel_requested_at TEXT;
ALTER TABLE assignments ADD COLUMN cancel_reason TEXT;

-- Stop's UPDATE targets "the live assignment(s) for this issue" — chat_id
-- (mission dispatches set ChatID = missions.id) OR group_id (also the
-- mission id, per scheduleTask) restricted to PENDING/RUNNING. Partial:
-- rows nobody asked to cancel are never this query's answer, and the vast
-- majority of assignments never get a Stop.
CREATE INDEX IF NOT EXISTS idx_assignment_cancel_requested
    ON assignments (cancel_requested_at)
    WHERE cancel_requested_at IS NOT NULL;
