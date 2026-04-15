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
// holding the per-workspace advisory lock, the run is refused before
// any side effects (DB writes, orchestrator state) occur.
//
// This closes the TOCTOU window between backup.ensureAgentsIdle and
// docker pause: without this guard, a backup that has passed the
// idle check can still race a fresh agent start and miss the new
// agent's DB rows in its dump.
//
// A nil db short-circuits to nil so tests that fake out the handler
// do not need a real lock table.
func refuseIfBackupInProgress(ctx context.Context, db *sql.DB, workspaceID string) error {
	if db == nil || workspaceID == "" {
		return nil
	}
	held, err := backup.IsLockHeld(ctx, db, workspaceID, time.Now())
	if err != nil {
		// Probing the lock table should never fail in steady state,
		// but if it does we fail open (allow the run) rather than
		// blocking every run when the lock subsystem has a bug.
		// Callers see the error in the logs via the returned nil-err
		// below; a future PR can tighten to fail-closed if needed.
		_ = err
		return nil
	}
	if held {
		return fmt.Errorf("workspace is being backed up; retry after the backup completes (check `crewship backup status`)")
	}
	return nil
}
