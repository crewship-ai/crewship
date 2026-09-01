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
// # NULL means the default; an explicit 0 means keep forever
//
// This mirrors workspaces.credential_audit_retention_days /
// audit_log_retention_days, NOT workspaces.run_retention_days — a reversal
// from this file's first version, which collapsed both NULL and 0 into the
// default. That was wrong for two reasons, not one:
//
//  1. approvals_retention_days sits on the SAME `workspace update` command
//     as the two audit flags, where 0 already means "keep forever". A
//     third retention flag on that command silently meaning something else
//     is a footgun by construction — an operator who types 0 out of
//     established habit with its neighbours would get 90-day deletion
//     instead of the "never delete" they asked for, with no error and no
//     warning. Consistency within one command's flag family outranks this
//     file's own internal tidiness argument.
//  2. The "journal_entries already has the durable record" argument this
//     comment used to make is only partly true: AfterDecide's journal entry
//     carries approval_id, kind and the decision comment — NOT `reason` or
//     the full `payload`. An operator who wants those to survive
//     indefinitely (e.g. auditing exactly what a destructive tool call's
//     arguments were, months later) has no other place to keep them, so
//     "keep forever" is a real, expressible operator intent here too, same
//     as it is for credential_audit.
//
// NULL still means "no opinion recorded, use the 90-day default" — that
// part is unchanged, and is why the default itself stays 90 rather than
// unlimited: nobody has to opt in just to get bounded growth on a freshly
// created workspace, only to opt OUT of pruning if they want the full
// history kept.
//
// # What is NEVER eligible, regardless of age
//
// Only rows in a terminal status (approved/denied/timeout/cancelled) with a
// non-NULL decided_at are candidates. A pending row is never touched no
// matter how old its created_at — not because deleting one would "strand"
// an agent's wait (it would not: gate.go's poll loop already treats a
// vanished row as denied and fails the wait deterministically, which is
// exactly the behaviour the GDPR erasure cascade added alongside this sweep
// relies on when IT deletes a pending row for a right-to-erasure request).
// The real reason is that a pending row is the operator's only handle on an
// async gate still awaiting a decision — retiring it is the timeout
// sweeper's job (store_sweep.go flips a stale pending row to 'timeout'),
// not retention's; retention only ever removes rows that are already
// terminal.
//
// # kind=autonomy_gate is carved out of the sweep entirely
//
// Every other kind's row is pure history once it is terminal: the decision
// is durably replayed elsewhere (journal_entries, reward-history) and the
// row itself no longer gates anything. kind=autonomy_gate is the one
// exception — for a mission target, the row IS the hold. Deny (or let the
// timeout sweeper flip) an autonomy_gate row and NO marker is written on the
// mission itself (applyAutonomyGateDecisionTx, internal_autonomy_gate.go);
// the mission stays PLANNING and the terminal approvals_queue row is the
// ONLY thing stopping POST .../missions/{id}/start from dispatching it
// (autonomyGateApproved + missions_internal.go's hasHold-not-approved 403).
// Sweep that row on the ordinary 90-day terminal-row clock and a denied
// mission starts running, unattended, three months late.
//
// sweepableApprovalKinds is therefore an ALLOWLIST, not a denylist. A
// denylist ("sweep everything except autonomy_gate") sweeps a brand new kind
// by default the moment someone adds one — silence is exactly how this bug
// shipped the first time. An allowlist fails the other way: a new kind is
// simply never swept until someone deliberately opts it in here, which is a
// missed cleanup, not a load-bearing row vanishing out from under a live
// authorization check. kind carries no CHECK constraint (see
// KindAutonomyGate's doc comment), so nothing else stops a new value from
// reaching this column.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	// DefaultApprovalsRetentionDays is applied when a workspace has no
	// approvals_retention_days override recorded (column is NULL). An
	// explicit 0 is NOT the same as NULL — see the package comment "NULL
	// means the default; an explicit 0 means keep forever".
	DefaultApprovalsRetentionDays = 90

	// MaxApprovalsRetentionDays is the largest value safe to convert to a
	// time.Duration in days: time.Duration is int64 nanoseconds, and
	// days*24h overflows past math.MaxInt64/(24*time.Hour) = 106751.99…,
	// wrapping NEGATIVE. A negative duration subtracted from now.UTC()
	// below would move `cutoff` into the FUTURE, and every terminal row —
	// however recent — would match `decided_at < cutoff`. Same arithmetic
	// pagePublicExpiry guards against in
	// internal/api/pages_public_tokens.go. The API write path
	// (internal/api/workspaces_mutate.go) rejects a larger value before it
	// is ever persisted; SweepApprovalsRetention enforces it again
	// defensively below in case a bad value reaches the column some other
	// way.
	MaxApprovalsRetentionDays = 106751

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

// sweepableApprovalKinds is the ALLOWLIST of approvals_queue.kind values the
// retention sweep is permitted to delete once a row is terminal and aged
// out. See the package comment "kind=autonomy_gate is carved out of the
// sweep entirely" for why this is an allowlist rather than "every kind
// except autonomy_gate": a new Kind must be deliberately added here before
// its terminal rows are ever eligible, so a kind nobody has reasoned about
// yet defaults to kept, not swept.
//
// KindAutonomyGate is deliberately absent — its terminal row is the sole
// gate on POST .../missions/{id}/start, not history (see the package
// comment). KindEphemeralHire is present: it was checked against the same
// question (does anything read a terminal row of this kind back as
// authoritative state?) and, unlike autonomy_gate, its own doc comment and
// store_sweep.go confirm the staged agent's own row carries its inert
// state — the approvals_queue row is not read back after the decision.
var sweepableApprovalKinds = []Kind{
	KindToolCall,
	KindCostThreshold,
	KindDestructiveOp,
	KindTargetEnvironment,
	KindCustom,
	KindEphemeralHire,
}

// SweepApprovalsRetention deletes terminal approvals_queue rows for one
// workspace whose decided_at is older than (now - retentionDays), in
// bounded batches. Returns the number deleted and whether it stopped at the
// batch cap with work remaining.
//
// retentionDays <= 0 means "delete nothing" — this function does not know
// or care whether the caller passed a literal 0 (explicit keep-forever) or
// resolved NULL down to something non-positive; either way, nothing is
// eligible. Resolving NULL to DefaultApprovalsRetentionDays before calling
// this is the caller's job (see SweepAllWorkspacesApprovalsRetention), which
// is what makes this function safe to call directly with a raw column value
// in tests.
//
// retentionDays > MaxApprovalsRetentionDays is refused rather than clamped
// or silently treated as "delete nothing": the write path
// (internal/api/workspaces_mutate.go) is supposed to have already rejected
// it, so a value this large reaching here means something bypassed that
// check, and that is worth surfacing as an error rather than papering over.
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
	if retentionDays > MaxApprovalsRetentionDays {
		// Refuse rather than compute a cutoff: retentionDays*24h overflows
		// int64 nanoseconds past this point and wraps NEGATIVE, which would
		// move `cutoff` into the future and match every terminal row
		// regardless of age — turning "keep for N days" into "delete
		// everything". See MaxApprovalsRetentionDays.
		return 0, false, fmt.Errorf(
			"harbormaster: sweep approvals retention: retention_days %d exceeds the maximum of %d days",
			retentionDays, MaxApprovalsRetentionDays)
	}

	// Same fixed-width format the column itself is written in (timeFmt,
	// store.go), so the string comparison below matches time order exactly
	// with no risk of the differing-precision mis-sort tsformat's doc
	// comment warns about.
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour).Format(timeFmt)

	// kind IN (...) is built from sweepableApprovalKinds rather than a fixed
	// placeholder count so the allowlist has exactly one place to edit — see
	// the package comment and sweepableApprovalKinds' doc comment.
	placeholders := make([]string, len(sweepableApprovalKinds))
	args := make([]any, 0, len(sweepableApprovalKinds)+3)
	args = append(args, workspaceID)
	for i, k := range sweepableApprovalKinds {
		placeholders[i] = "?"
		args = append(args, string(k))
	}
	args = append(args, cutoff, approvalsRetentionBatchRows)

	stmt := `DELETE FROM approvals_queue WHERE id IN (
		SELECT id FROM approvals_queue
		WHERE workspace_id = ?
		  AND status IN ('approved', 'denied', 'timeout', 'cancelled')
		  AND kind IN (` + strings.Join(placeholders, ", ") + `)
		  AND decided_at IS NOT NULL
		  AND decided_at < ?
		LIMIT ?
	)`

	for i := 0; i < approvalsRetentionMaxBatches; i++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return deleted, false, ctxErr
		}
		res, execErr := db.ExecContext(ctx, stmt, args...)
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
// approvals_queue using that workspace's configured window: NULL resolves to
// DefaultApprovalsRetentionDays, a recorded 0 means keep forever (deletes
// nothing), and n > 0 means n days. Errors are accumulated rather than
// returned on the first failure, so one bad workspace does not stop the
// sweep for the rest — same as SweepAllWorkspacesAuditRetention.
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
		// NULL (days.Valid == false) → no opinion recorded, use the
		// default. A recorded 0 is an explicit "keep forever" and must
		// pass through as 0, not collapse into the default — see the
		// package comment. SweepApprovalsRetention already treats any
		// retentionDays <= 0 as "delete nothing", so passing 0 through
		// is sufficient; no separate keep-forever branch is needed here.
		e := wsWindow{id: id, days: DefaultApprovalsRetentionDays}
		if days.Valid {
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
