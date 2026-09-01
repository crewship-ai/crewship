package harbormaster

// Per-workspace retention sweep for terminal approvals_queue rows (#2233).
//
// Before this, every terminal transition (approve/deny at store_mutate.go,
// cancel at store_mutate.go, the timeout sweeper at store_sweep.go, the
// sync-gate timeout at gate.go) was an in-place UPDATE of status/decided_*.
// Nothing ever deleted a row: `grep -rn "DELETE FROM approvals"
// --include=*.go` over non-test Go returned nothing. Rows are also
// IntentInclude for backup, so they survived into every bundle and every
// restore, forever, by default.
//
// Shape copied from internal/api/audit_retention.go (its own header says it
// follows pipeline.SweepRunRetention): a nullable per-workspace override
// column, a cutoff computed from "now minus N days", a batched DELETE capped
// per tick so a large backlog cannot monopolise the single SQLite writer
// lock, and a daily background ticker with an immediate first sweep.
//
// # Why the default is 90 days, not "keep forever"
//
// audit_logs (internal/api/audit_retention.go) defaults to UNLIMITED because
// it is the compliance trail an operator may have a legal duty to retain,
// and docs/security/gdpr.mdx frames it that way explicitly. approvals_queue
// is not that table: every terminal decision it records is ALSO durably
// captured in journal_entries (AfterDecide emits an EntryApprovalGranted /
// EntryApprovalDenied entry with the same approval_id, decider and comment)
// and in the reward-history table that feeds AdjustMode. approvals_queue
// itself is the operational working set the HITL inbox pages through — the
// same role pipeline_runs and credential_audit play, both of which default
// to 90 days (DefaultRunRetentionDays, DefaultCredentialAuditRetentionDays).
// One shared number for "how long we keep operational history" beats a
// fourth number to independently justify.
//
// # Why NULL/<=0 means "use the default" rather than "keep forever"
//
// This deliberately follows workspaces.run_retention_days rather than
// workspaces.credential_audit_retention_days. The audit pair needed an
// explicit "0 = keep forever" because collapsing it into the default would
// make switching off compliance-record pruning inexpressible — a real
// operator need for a table with a legal retention story. approvals_queue
// has no such story (see above: the durable record is journal_entries), so
// there is no operator intent "0" needs to carry that NULL doesn't already
// carry via the default. Keeping one fewer state to reason about wins.
//
// # What is NEVER eligible, regardless of age
//
// Only rows in a terminal status (approved/denied/timeout/cancelled) with a
// non-NULL decided_at are candidates. A pending row is never touched no
// matter how old its created_at — the timeout sweeper (store_sweep.go) is
// what retires a stale pending row to 'timeout'; deleting a still-pending
// approval out from under an agent waiting on it would strand the wait
// forever instead of failing it deterministically.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	// DefaultApprovalsRetentionDays is applied when a workspace has no
	// approvals_retention_days override (NULL or <= 0). See the package
	// comment for why this mirrors DefaultRunRetentionDays /
	// DefaultCredentialAuditRetentionDays rather than "keep forever".
	DefaultApprovalsRetentionDays = 90

	// approvalsRetentionBatchRows bounds how many rows a single DELETE may
	// touch, same rationale as auditRetentionBatchRows in
	// internal/api/audit_retention.go: SQLite holds the one database-wide
	// write lock for the whole statement, so an unbounded DELETE over a
	// years-old backlog would stall every agent for its duration.
	approvalsRetentionBatchRows = 5000

	// approvalsRetentionMaxBatches bounds one workspace's sweep per tick so
	// a first run against years of history cannot monopolise the writer.
	// 100 * 5000 = 500,000 rows per workspace per day, far above any
	// realistic daily accrual of decided approvals.
	approvalsRetentionMaxBatches = 100
)

// SweepApprovalsRetention deletes terminal approvals_queue rows for one
// workspace whose decided_at is older than (now - retentionDays), in
// bounded batches. Returns the number deleted and whether it stopped at the
// batch cap with work remaining.
//
// retentionDays <= 0 means "use the default" is the CALLER's job to resolve
// (see SweepAllWorkspacesApprovalsRetention) — this function treats <= 0 as
// "delete nothing" so it is safe to call directly with a raw column value in
// tests without re-deriving the default.
func SweepApprovalsRetention(
	ctx context.Context,
	db *sql.DB,
	workspaceID string,
	retentionDays int,
) (deleted int, capped bool, err error) {
	if db == nil {
		return 0, false, errors.New("harbormaster: sweep approvals retention: db is nil")
	}
	if workspaceID == "" {
		return 0, false, errors.New("harbormaster: sweep approvals retention: workspace_id required")
	}
	if retentionDays <= 0 {
		return 0, false, nil
	}

	// Same fixed-width format the column itself is written in (timeFmt,
	// store.go), so the string comparison below matches time order exactly
	// with no risk of the differing-precision mis-sort tsformat's doc
	// comment warns about.
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour).Format(timeFmt)

	const stmt = `DELETE FROM approvals_queue WHERE id IN (
		SELECT id FROM approvals_queue
		WHERE workspace_id = ?
		  AND status IN ('approved', 'denied', 'timeout', 'cancelled')
		  AND decided_at IS NOT NULL
		  AND decided_at < ?
		LIMIT ?
	)`

	for i := 0; i < approvalsRetentionMaxBatches; i++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return deleted, false, ctxErr
		}
		res, execErr := db.ExecContext(ctx, stmt, workspaceID, cutoff, approvalsRetentionBatchRows)
		if execErr != nil {
			return deleted, false, fmt.Errorf("harbormaster: sweep approvals retention: %w", execErr)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
		if n < approvalsRetentionBatchRows {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

// SweepAllWorkspacesApprovalsRetention enumerates every workspace and sweeps
// approvals_queue using that workspace's configured window (or
// DefaultApprovalsRetentionDays when NULL or <= 0). Errors are accumulated
// rather than returned on the first failure, so one bad workspace does not
// stop the sweep for the rest — same as SweepAllWorkspacesAuditRetention.
func SweepAllWorkspacesApprovalsRetention(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if db == nil {
		return errors.New("harbormaster: sweep approvals retention: db is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}

	rows, err := db.QueryContext(ctx, `SELECT id, approvals_retention_days FROM workspaces`)
	if err != nil {
		return fmt.Errorf("harbormaster: list workspaces: %w", err)
	}
	type wsWindow struct {
		id   string
		days int
	}
	var entries []wsWindow
	for rows.Next() {
		var id string
		var days sql.NullInt64
		if scanErr := rows.Scan(&id, &days); scanErr != nil {
			rows.Close()
			return fmt.Errorf("harbormaster: scan workspace row: %w", scanErr)
		}
		e := wsWindow{id: id, days: DefaultApprovalsRetentionDays}
		if days.Valid && days.Int64 > 0 {
			e.days = int(days.Int64)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("harbormaster: iterate workspaces: %w", rowsErr)
	}

	var errs []error
	for _, e := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		n, capped, sweepErr := SweepApprovalsRetention(ctx, db, e.id, e.days)
		if sweepErr != nil {
			errs = append(errs, fmt.Errorf("workspace %s: %w", e.id, sweepErr))
			continue
		}
		if n > 0 {
			logger.Info("approvals retention sweep",
				"workspace_id", e.id, "deleted", n, "retention_days", e.days)
		}
		if capped {
			logger.Warn("approvals retention sweep stopped at its batch cap",
				"workspace_id", e.id, "deleted", n, "max_batches", approvalsRetentionMaxBatches,
				"note", "backlog outran one tick; it resumes on the next sweep")
		}
	}
	return errors.Join(errs...)
}

// StartApprovalsRetentionSweeper runs SweepAllWorkspacesApprovalsRetention
// every `interval`, with an immediate first sweep so a freshly started
// server does not wait a full day. Blocks; callers run it as a goroutine.
// Not leader-gated, matching StartAuditRetentionSweeper and pipeline's
// StartRunRetentionSweeper — see their doc comments for why a multi-replica
// deployment sweeping redundantly is wasteful but not incorrect.
func StartApprovalsRetentionSweeper(ctx context.Context, db *sql.DB, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := SweepAllWorkspacesApprovalsRetention(ctx, db, logger); err != nil {
		logger.Warn("approvals retention sweeper: initial sweep failed", "error", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := SweepAllWorkspacesApprovalsRetention(ctx, db, logger); err != nil {
				logger.Warn("approvals retention sweeper: tick failed", "error", err)
			}
		}
	}
}
