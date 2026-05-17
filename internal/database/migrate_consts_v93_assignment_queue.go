package database

// migrationAddAssignmentQueue (v93) lays the schema groundwork for the
// per-crew admission queue described in
// .claude/context/prd/QUEUE-MECHANISM-2026.md.
//
// Three additive changes — none of them touch existing rows beyond a
// NULL default, so the upgrade is non-blocking and a downgrade is a
// straight ALTER … DROP COLUMN (if SQLite ever permits it, which it
// doesn't natively today — operators on a downgrade path restore from
// a pre-v93 backup).
//
//  1. assignments.queued_at         — set when the dispatcher's
//     atomic claimCrewSlot CAS fails (budget full); paired with the
//     'QUEUED' status string. NULL on every existing row → those
//     rows were never queued.
//
//  2. assignments.running_at        — set by the same CAS that flips
//     status to 'RUNNING'. We already have started_at, but that's
//     populated by the agent process after it boots; running_at is
//     the dispatcher-side stamp so a row can be in RUNNING for a
//     measurable interval before the agent fires its own start
//     event. Without this column the queue dwell-time metric
//     ("how long was I QUEUED before I ran?") is unobservable.
//
//  3. crews.max_concurrent_agents   — operator override for the
//     computed default (floor(container_memory_mb /
//     agent_memory_estimate_mb)). NULL → use the computed value.
//     Non-NULL → trust the operator. The CHECK below guards the
//     two trap configurations:
//       - 0 would deadlock the queue (claimCrewSlot can never
//         succeed; every dispatch goes QUEUED forever).
//       - Negative is nonsensical.
//
// We intentionally do NOT add a CHECK on assignments.status to
// constrain the enum to {PENDING, QUEUED, RUNNING, COMPLETED, FAILED,
// CANCELLED}. The existing table accepts arbitrary strings (no CHECK
// in v01_init.go), and adding one now means a full table rebuild on
// upgrade — slow on hot deployments and unnecessary because every
// writer in the codebase already passes a value from the known set.
// If we ever need to enforce, do it in a separate v94 with a proper
// rebuild migration. agent_runs.status is the same story.
//
// Index: idx_assignments_status_queued_at is the queue-pump's read
// path. It supports
//
//	SELECT id FROM assignments
//	 WHERE status = 'QUEUED'
//	 ORDER BY queued_at ASC
//	 LIMIT 1
//
// scoped to a specific crew (via the assigned_to_id → agents.crew_id
// JOIN). The partial-index WHERE clause keeps the index tiny in
// steady state — under load most QUEUED rows transition out within
// seconds and only the unclaimed tail lives in the index.
const migrationAddAssignmentQueue = `
ALTER TABLE assignments ADD COLUMN queued_at TEXT;
ALTER TABLE assignments ADD COLUMN running_at TEXT;
ALTER TABLE crews
    ADD COLUMN max_concurrent_agents INTEGER
        CHECK (max_concurrent_agents IS NULL OR max_concurrent_agents > 0);
CREATE INDEX IF NOT EXISTS idx_assignments_status_queued_at
    ON assignments(status, queued_at)
    WHERE status = 'QUEUED';
`
