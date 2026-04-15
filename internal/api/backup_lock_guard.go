package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// refuseIfBackupInProgress is the shared guard called from every
// request path that triggers a new agent run. If a backup is currently
// holding the per-workspace advisory lock — OR a backup is mid-flight
// in this process and has not yet written its lock row — the run is
// refused before any side effects (DB writes, orchestrator state)
// occur.
//
// Design — TOCTOU closure
// ------------------------
// Historically this guard was a bare SELECT against backup_locks,
// which left a race window: a request could pass the check, then the
// backup could claim the DB lock and run ensureAgentsIdle without
// seeing the about-to-start run.
//
// We close the race with an in-process WorkspaceGuard
// (internal/backup/guard.go). BeginMission is ATOMIC w.r.t.
// BeginBackup: once a backup holds the guard, all BeginMission calls
// fail fast with ErrGuardBackupInProgress. Conversely, as long as any
// mission holds the guard, BeginBackup fails with
// ErrGuardMissionsInFlight. The caller MUST invoke the returned
// release exactly once (via defer), ideally after the run has fully
// registered itself with the orchestrator so a subsequent backup's
// ensureAgentsIdle can see it.
//
// Crewship runs as a single binary, so a process-local guard combined
// with the durable DB row is sufficient. If the architecture ever
// splits across processes, this must be replaced with a DB-backed
// advisory lock that mission-start and backup-start BOTH contend for
// inside the same transaction that registers the mission row.
//
// A nil db short-circuits to (no-op release, nil) so tests that fake
// the handler do not need a real lock table.
func refuseIfBackupInProgress(ctx context.Context, db *sql.DB, workspaceID string) (release func(), err error) {
	if workspaceID == "" {
		return func() {}, nil
	}
	// Check the durable DB row first. This catches a backup that was
	// started by another process (historical / future) or a backup
	// already in its "stream payload" phase without holding the
	// in-process guard (defence-in-depth).
	if db != nil {
		held, probeErr := backup.IsLockHeld(ctx, db, workspaceID, time.Now())
		if probeErr != nil {
			// Probing the lock table should never fail in steady state,
			// but if it does we fail open (allow the run) rather than
			// blocking every run when the lock subsystem has a bug. A
			// lock-subsystem incident surfaces via `crewship backup
			// status` and the audit trail, not via the run log.
			_ = probeErr
		} else if held {
			return nil, fmt.Errorf("workspace is being backed up; retry after the backup completes (check `crewship backup status`)")
		}
	}
	// Atomically claim the mission side of the in-process guard.
	rel, err := backup.DefaultGuard().BeginMission(workspaceID)
	if err != nil {
		return nil, err
	}
	return rel, nil
}
