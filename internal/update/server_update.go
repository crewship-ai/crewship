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
	// Prepare downloads + verifies the new release and stages it, WITHOUT
	// touching the running install. It runs BEFORE the service is stopped, so a
	// download or checksum failure never causes downtime. It returns an opaque
	// handle passed straight to Commit (in production, *PreparedUpdate).
	Prepare func(ctx context.Context) (any, error)
	// Commit swaps the staged binary (+ companions) onto disk while the service
	// is stopped, and returns what it swapped so a later rollback can restore
	// exactly those files. On failure it must leave the previous binary intact
	// (PreparedUpdate.Commit self-rolls-back a partial swap), so a Commit error
	// means "binary unchanged".
	Commit func(ctx context.Context, prepared any) (*SelfUpdateResult, error)
	// Health verifies a running server is serving traffic. Called for the new
	// binary and again after a rollback (to catch a rolled-back server that is
	// itself broken — e.g. a post-migration schema-skew crash loop).
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
	Result     *SelfUpdateResult // the swap that was applied (nil if prepare/commit failed)
	Healthy    bool              // the new binary started and passed its health check
	RolledBack bool              // the previous binary was restored and restarted
}

// snapshotRestoreNow is the urgent guidance when a rolled-back server is ALSO
// unhealthy after a post-migration failure: the new binary already ran
// forward-only migrations, so the restored older binary trips the version-skew
// guard and crash-loops until its schema is restored too.
const snapshotRestoreNow = "the new binary already migrated the database before failing, so the restored " +
	"older binary cannot boot against the newer schema (version-skew guard). Restore the pre-migration " +
	"snapshot NOW to recover: 'crewship db restore-snapshot'"

// snapshotRollbackHint is the softer note for a post-migration rollback whose
// old binary DID come back healthy — the operator may still want the snapshot
// if they intend to stay on the old version.
const snapshotRollbackHint = "note: the new binary may have migrated the database on start; if you stay on " +
	"the previous binary and it later refuses to boot with a version-skew error, restore the pre-migration " +
	"snapshot: 'crewship db restore-snapshot'"

// RunServerUpdate orchestrates an in-place upgrade of a service-managed
// crewship install: prepare → stop → swap → start → health-check, with
// automatic rollback to the previous binary if the new one fails. It is
// deliberately clock-free and side-effect-free beyond the injected deps so the
// full state machine is unit-testable.
//
// Failure modes and their guarantees:
//   - prepare fails         → download/verify failed; server never stopped.
//   - stop fails            → nothing swapped; return error, server untouched.
//   - commit fails          → binary unchanged; restart the old binary so the
//     server isn't left down; return error.
//   - new binary won't start→ roll back to the previous binary, restart it, and
//     probe it.
//   - new binary unhealthy  → stop it, roll back, restart the old binary, probe
//     it; if the old binary is also unhealthy, surface the urgent
//     restore-snapshot-NOW guidance (migrations ran).
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

	// 1. Prepare (download + verify) BEFORE stopping anything. A failure here
	// never touches the running server — the whole point of pre-fetching.
	logf("Downloading and verifying the new release…")
	prepared, err := deps.Prepare(ctx)
	if err != nil {
		return out, fmt.Errorf("prepare update failed (server untouched): %w", err)
	}

	// 2. Stop the running server before touching its binary — a swap under a
	// live process is how you get a half-updated server.
	logf("Stopping the crewship service…")
	if err := deps.Manager.Stop(ctx); err != nil {
		return out, fmt.Errorf("could not stop the crewship service (binary unchanged): %w", err)
	}

	// 3. Swap the binary. A failure here leaves the previous binary in place
	// (Commit rolls back its own partial swap), so we just bring the old server
	// back up rather than leaving it down.
	logf("Swapping the crewship binary…")
	result, err := deps.Commit(ctx, prepared)
	if err != nil {
		if startErr := deps.Manager.Start(ctx); startErr != nil {
			return out, fmt.Errorf(
				"swap failed: %w; AND restarting the previous binary failed: %v — "+
					"start the service manually", err, startErr)
		}
		return out, fmt.Errorf("swap failed (previous binary restored and restarted): %w", err)
	}
	out.Result = result

	// 4. Start the new binary. If it can't even start, roll back to the
	// previous binary and restart that.
	logf(fmt.Sprintf("Starting crewship %s…", result.ToVersion))
	if err := deps.Manager.Start(ctx); err != nil {
		rolledBack, rbErr := rollbackTo(ctx, deps, rollback, result,
			fmt.Errorf("the new binary failed to start: %w", err), false)
		out.RolledBack = rolledBack
		return out, rbErr
	}

	// 5. Health-check the new server. If it started but isn't serving, treat it
	// the same as a failed start — but the rollback path flags the snapshot,
	// since migrations run on start.
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

// rollbackTo restores the previous binary from backups, restarts the service,
// and probes that the restored server is actually healthy. postMigration is
// true when the new binary had already started (so forward-only migrations may
// have run) — in that case an unhealthy rolled-back server is almost certainly
// the version-skew guard, and the error escalates to restore-snapshot-NOW. It
// returns whether the previous binary was successfully restored AND restarted,
// plus the error the caller should surface.
func rollbackTo(ctx context.Context, deps ServerUpdateDeps, rollback func([]string) error, result *SelfUpdateResult, cause error, postMigration bool) (bool, error) {
	if deps.Log != nil {
		deps.Log("Rolling back to the previous binary…")
	}

	if err := rollback(result.Replaced); err != nil {
		hint := ""
		if postMigration {
			hint = "\n" + snapshotRestoreNow
		}
		return false, fmt.Errorf(
			"%w; AND rollback failed: %v — manual recovery required: restore from %s%s",
			cause, err, result.BackupPath, hint)
	}
	// Binary restored; bring the old server back up.
	if err := deps.Manager.Start(ctx); err != nil {
		hint := ""
		if postMigration {
			hint = "\n" + snapshotRestoreNow
		}
		return false, fmt.Errorf(
			"%w; rolled back to the previous binary but restarting it failed: %v — "+
				"start the service manually%s",
			cause, err, hint)
	}

	// Confirm the rolled-back server actually recovered. A post-migration
	// rollback can leave the OLD binary crash-looping on the skew guard because
	// the new binary already migrated the DB — the operator must restore the
	// snapshot immediately, so we probe rather than reporting a false recovery.
	if deps.Health != nil {
		if herr := deps.Health(ctx); herr != nil {
			if postMigration {
				return true, fmt.Errorf(
					"%w; rolled back to the previous binary but it is ALSO unhealthy (%v) — %s",
					cause, herr, snapshotRestoreNow)
			}
			return true, fmt.Errorf(
				"%w; rolled back to the previous binary but it did not become healthy (%v) — "+
					"check the service logs",
				cause, herr)
		}
	}

	if postMigration {
		return true, fmt.Errorf("%w (rolled back to the previous binary; %s)", cause, snapshotRollbackHint)
	}
	return true, fmt.Errorf("%w (rolled back to the previous binary)", cause)
}
