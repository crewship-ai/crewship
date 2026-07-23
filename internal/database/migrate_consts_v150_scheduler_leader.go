package database

// migrationSchedulerLeader (v150) adds the single-row lease table that backs
// scheduler leader election (issue #1376). Before multi-replica deploys, the
// agent cron scheduler, the pipeline cron scheduler, and the recurring-issue
// dispatcher would each tick on every replica and double-fire; the lease lets
// exactly one replica hold leadership and act.
//
// The row is keyed by `scope` (one lease gates all scheduling loops in a
// process). holder_id identifies the owning replica; expires_at/acquired_at are
// unix-second integers written from the DATABASE clock (not any replica's wall
// clock) so arbitration is skew-proof across hosts — see internal/leader.
//
// This is instance-local runtime state (like pipeline_run_idempotency): it is
// NOT workspace-scoped, so it needs no BackupTableIntent entry and never
// travels in a backup bundle. Additive + idempotent (CREATE TABLE IF NOT
// EXISTS), so restore replays are safe.
const migrationSchedulerLeader = `
CREATE TABLE IF NOT EXISTS scheduler_leader (
    scope       TEXT PRIMARY KEY,
    holder_id   TEXT NOT NULL,
    acquired_at INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
`
