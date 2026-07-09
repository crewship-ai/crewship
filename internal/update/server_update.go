package update

import (
	"context"
	"fmt"
)

// ServiceManager abstracts the init system that controls a long-running
// crewship server (systemd in production). Extracted behind an interface so
// the upgrade orchestration below is unit-testable with a mock instead of a
// live service manager.
type ServiceManager interface {
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

// HealthChecker blocks until the freshly started server is serving, or returns
// an error once its own deadline elapses. Encapsulating the polling here keeps
// RunServerUpdate a pure state machine (no clock) — the production checker
// polls the health endpoint; tests inject an instant pass/fail.
type HealthChecker func(ctx context.Context) error

// ServerUpdateDeps are the injectable collaborators of RunServerUpdate.
type ServerUpdateDeps struct {
	// Manager stops/starts the crewship service around the swap.
	Manager ServiceManager
	// Swap replaces the on-disk binary (+ companions) and returns what it
	// swapped so a later rollback can restore exactly those files. In
	// production this wraps ApplyInstallerUpdate; on failure it must leave the
	// previous binary intact (ApplyInstallerUpdate self-rolls-back a partial
	// swap), so a Swap error means "binary unchanged".
	Swap func(ctx context.Context) (*SelfUpdateResult, error)
	// Health verifies the new server booted and serves traffic.
	Health HealthChecker
	// Rollback restores the given paths from their backups (defaults to
	// RestoreBackups). Invoked only when the NEW binary was swapped in but then
	// failed to start or came up unhealthy.
	Rollback func(paths []string) error
	// Log receives human-readable progress lines (defaults to a no-op).
	Log func(string)
}

// ServerUpdateOutcome reports how a server upgrade ended.
type ServerUpdateOutcome struct {
	Result     *SelfUpdateResult // the swap that was applied (nil if swap failed)
	Healthy    bool              // the new binary started and passed its health check
	RolledBack bool              // the previous binary was restored and restarted
}

// snapshotRollbackHint is the schema half of a rollback: the new binary runs
// forward-only migrations on its first start, so if it started but then failed
// its health check the DB may already be at a newer schema than the restored
// old binary understands. Restoring the binary alone would then trip the
// version-skew guard on the old binary's next start.
const snapshotRollbackHint = "the new binary may have already migrated the database on start; " +
	"if the restored server refuses to boot with a version-skew error, also restore the " +
	"pre-migration snapshot: 'crewship db restore-snapshot'"

// RunServerUpdate orchestrates an in-place upgrade of a service-managed
// crewship install: stop → swap → start → health-check, with automatic
// rollback to the previous binary if the new one fails to start or comes up
// unhealthy. It is deliberately clock-free and side-effect-free beyond the
// injected deps so the full state machine is unit-testable.
//
// Failure modes and their guarantees:
//   - stop fails            → nothing swapped; return error, server untouched.
//   - swap fails            → binary unchanged; restart the old binary so the
//     server isn't left down; return error.
//   - new binary won't start→ roll back to the previous binary and restart it.
//   - new binary unhealthy  → stop it, roll back, restart the old binary, and
//     surface the restore-snapshot hint (migrations may have run).
//   - rollback itself fails → return a critical error naming manual recovery.
func RunServerUpdate(ctx context.Context, deps ServerUpdateDeps) (*ServerUpdateOutcome, error) {
	logf := deps.Log
	if logf == nil {
		logf = func(string) {}
	}
	rollback := deps.Rollback
	if rollback == nil {
		rollback = RestoreBackups
	}
	out := &ServerUpdateOutcome{}

	// 1. Stop the running server before touching its binary — a swap under a
	// live process is how you get a half-updated server.
	logf("Stopping the crewship service…")
	if err := deps.Manager.Stop(ctx); err != nil {
		return out, fmt.Errorf("could not stop the crewship service (binary unchanged): %w", err)
	}

	// 2. Swap the binary. A failure here leaves the previous binary in place
	// (ApplyInstallerUpdate rolls back its own partial swap), so we just bring
	// the old server back up rather than leaving it down.
	logf("Swapping the crewship binary…")
	result, err := deps.Swap(ctx)
	if err != nil {
		if startErr := deps.Manager.Start(ctx); startErr != nil {
			return out, fmt.Errorf(
				"swap failed: %w; AND restarting the previous binary failed: %v — "+
					"start the service manually", err, startErr)
		}
		return out, fmt.Errorf("swap failed (previous binary restored and restarted): %w", err)
	}
	out.Result = result

	// 3. Start the new binary. If it can't even start, roll back to the
	// previous binary and restart that.
	logf(fmt.Sprintf("Starting crewship %s…", result.ToVersion))
	if err := deps.Manager.Start(ctx); err != nil {
		rolledBack, rbErr := rollbackTo(ctx, deps, rollback, result,
			fmt.Errorf("the new binary failed to start: %w", err), false)
		out.RolledBack = rolledBack
		return out, rbErr
	}

	// 4. Health-check the new server. If it started but isn't serving, treat it
	// the same as a failed start — but also flag the snapshot, since migrations
	// run on start.
	logf("Waiting for the new server to become healthy…")
	if err := deps.Health(ctx); err != nil {
		// Stop the unhealthy new server before restoring the old binary.
		_ = deps.Manager.Stop(ctx)
		rolledBack, rbErr := rollbackTo(ctx, deps, rollback, result,
			fmt.Errorf("the new binary started but failed its health check: %w", err), true)
		out.RolledBack = rolledBack
		return out, rbErr
	}

	out.Healthy = true
	logf(fmt.Sprintf("Upgrade complete: crewship %s → %s is healthy.", result.FromVersion, result.ToVersion))
	return out, nil
}

// rollbackTo restores the previous binary from backups and restarts the
// service, composing the resulting (possibly compound) error. withSnapshotHint
// appends the migration/restore-snapshot guidance for the unhealthy path. It
// returns whether the previous binary was successfully restored AND restarted,
// plus the error the caller should surface.
func rollbackTo(ctx context.Context, deps ServerUpdateDeps, rollback func([]string) error, result *SelfUpdateResult, cause error, withSnapshotHint bool) (bool, error) {
	if deps.Log != nil {
		deps.Log("Rolling back to the previous binary…")
	}
	hint := ""
	if withSnapshotHint {
		hint = "\n" + snapshotRollbackHint
	}

	if err := rollback(result.Replaced); err != nil {
		return false, fmt.Errorf(
			"%w; AND rollback failed: %v — manual recovery required: restore from %s%s",
			cause, err, result.BackupPath, hint)
	}
	// Binary restored; bring the old server back up.
	if err := deps.Manager.Start(ctx); err != nil {
		return false, fmt.Errorf(
			"%w; rolled back to the previous binary but restarting it failed: %v — "+
				"start the service manually%s",
			cause, err, hint)
	}
	return true, fmt.Errorf("%w (rolled back to the previous binary)%s", cause, hint)
}
