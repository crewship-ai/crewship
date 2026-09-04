-- assignments.lease_owner / lease_expires_at (PRD-ISSUES-AND-ROUTINES-2026
-- §9.4, work package B4 — #2343).
--
-- F8: "No lease. Recovery is by process-start timestamp." RecoverInterrupted-
-- Running fails every RUNNING row older than the recovering process's own
-- boot time — sound for exactly one process, and actively wrong for two:
-- nothing records WHICH process owns a run, so a second replica's boot would
-- fail the first replica's still-live runs. A lease closes that gap: the
-- process actually driving a run stamps who it is and renews a deadline
-- while it works: `internal/api/assignments_run.go`'s runAssignment (the
-- "Mark assignment as RUNNING" stamp, then a heartbeat goroutine wrapping
-- the RunAgentForAssignment exec). Recovery then asks "has the deadline
-- passed", never "did I start after this row did" — a row a healthy replica
-- is actively heartbeating is never touched, no matter how old the
-- recovering process's own boot time is.
--
-- lease_owner: a free-text process identity (hostname:pid) — diagnostic,
-- not the correctness mechanism. Correctness comes from lease_expires_at
-- alone: only the true owner is renewing it, so an expired deadline is
-- true regardless of whether a second process can independently verify who
-- the (possibly dead) owner was. Nullable: a row this process never claimed
-- (dispatched before this migration, or mid-claim in the instant before the
-- RUNNING stamp runs) legitimately names no owner.
--
-- lease_expires_at: same RFC3339 UTC-string convention as every other
-- assignments timestamp column (started_at, running_at). NULL means "no
-- lease has ever been stamped on this row" — recovery falls back to the
-- pre-B4 process-start heuristic for exactly those rows (legacy data; every
-- row this process dispatches from here on gets one), never to "treat NULL
-- as already expired", which would fail a live run the instant it claims
-- its slot and before its first heartbeat has had a chance to land.
--
-- Partial index, WHERE status='RUNNING' only: lease_expires_at is
-- meaningless once a row is terminal (or still PENDING/QUEUED, which have
-- no driver to hold a lease at all), so a full index would carry rows the
-- sweeper's own query (`WHERE status='RUNNING' AND lease_expires_at < ?`)
-- never asks about. Same shape as idx_assignment_cancel_requested and
-- idx_assignments_lease_expires's sibling in §9.4's own index list.
ALTER TABLE assignments ADD COLUMN lease_owner TEXT;
ALTER TABLE assignments ADD COLUMN lease_expires_at TEXT;

CREATE INDEX IF NOT EXISTS idx_assignments_lease_expires
    ON assignments (lease_expires_at)
    WHERE status = 'RUNNING';
