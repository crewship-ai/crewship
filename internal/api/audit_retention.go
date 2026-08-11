package api

// Retention for the two audit tables that had none.
//
// pipeline_runs (v158), inbox_items and journal_entries (compaction) are all
// pruned. credential_audit and audit_logs never were — nothing in the tree
// deleted from either. credential_audit gains a row on every credential read
// by every agent, audit_logs on every entity mutation, so both grow linearly
// with traffic and never shrink. On a dev instance with almost no real use
// credential_audit was already the second-largest table in the database.
//
// The two defaults differ on purpose, and the difference is the design:
//
//   - credential_audit is operational telemetry. 90 days of it is plenty,
//     because the answer an operator actually reads — last_used_at and the
//     last_used_ips ring — is denormalised onto `credentials` and untouched
//     by this. A 90-day window loses detail, not "is this credential in use".
//
//   - audit_logs is the compliance trail, and it defaults to UNLIMITED.
//     docs/security/gdpr.mdx says audit records "have to survive operator's
//     own retention obligations". Crewship is self-hosted: the operator knows
//     their legal duty and we do not, and a product that silently deletes
//     compliance records by default is a footgun that only surfaces at an
//     audit. The mechanism ships; choosing to use it is the operator's, via
//     workspaces.audit_log_retention_days.
//
// Shape follows pipeline.SweepRunRetention (internal/pipeline/retention.go):
// a per-workspace sweep, a per-workspace nullable override column, and one
// daily background ticker.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const (
	// DefaultCredentialAuditRetentionDays is applied when a workspace has no
	// credential_audit_retention_days override. 90 matches
	// pipeline.DefaultRunRetentionDays — one number for "how long we keep
	// operational history" rather than a second one to reason about.
	DefaultCredentialAuditRetentionDays = 90

	// DefaultAuditLogRetentionDays of 0 means NEVER DELETE. See the package
	// comment: this is the compliance trail, and the retention obligation
	// belongs to the operator. Setting workspaces.audit_log_retention_days
	// to a positive number opts a workspace in.
	DefaultAuditLogRetentionDays = 0

	// auditRetentionBatchRows bounds how many rows one DELETE statement may
	// touch. SQLite holds the single database-wide write lock for the whole
	// statement, so an unbounded DELETE over a years-old backlog would stall
	// every agent for its duration — measured elsewhere in this codebase at
	// 486ms for 50,000 rows on a comparable table.
	//
	// This is NOT the "chunk everything" pattern. Splitting a large
	// infrequent job into many small transactions measured WORSE (a daily
	// 20,000-row sweep went 150ms → 405ms, and a live writer's p95 4.1ms →
	// 21.5ms) because re-acquiring the lock costs more than the shorter holds
	// save. What this does is cap the worst-case hold of each statement while
	// keeping the count of statements low.
	auditRetentionBatchRows = 5000

	// auditRetentionMaxBatches bounds one table, one workspace, one tick, so
	// the first sweep on an instance with years of history cannot monopolise
	// the writer. 100 × 5000 = 500,000 rows per table per workspace per day,
	// far above any realistic daily accrual, so in steady state the cap never
	// fires. When it does, the remaining backlog is logged — no silent caps —
	// and the next tick continues.
	auditRetentionMaxBatches = 100

	// auditLogUnboundedWarnRows is the size at which an unlimited
	// audit_log_retention_days stops being a considered choice and starts
	// being one nobody revisited. Warned about once per sweep so the signal
	// reaches an operator who never set the column.
	auditLogUnboundedWarnRows = 1_000_000
)

// auditRetentionTable describes one sweepable audit table. Both are swept by
// the same code because the only differences are the table name, its
// timestamp column and its default — keeping them as data rather than two
// near-identical functions means a fix to the batching applies to both.
type auditRetentionTable struct {
	table   string
	tsCol   string
	defDays int
	// wsCol is the tenant column. credential_audit's is nullable (it was
	// added by 20260810153104 and backfilled), so orphan rows are handled
	// separately below.
	wsColNullable bool
}

var (
	credentialAuditRetention = auditRetentionTable{
		table: "credential_audit", tsCol: "occurred_at",
		defDays: DefaultCredentialAuditRetentionDays, wsColNullable: true,
	}
	auditLogRetention = auditRetentionTable{
		table: "audit_logs", tsCol: "created_at",
		defDays: DefaultAuditLogRetentionDays,
	}
)

// sweepAuditTable deletes rows for one workspace older than the window, in
// bounded batches. Returns the number deleted and whether it stopped at the
// batch cap with work remaining.
//
// retentionDays <= 0 means "keep forever" and deletes nothing — that is the
// audit_logs default, not an error.
func sweepAuditTable(
	ctx context.Context,
	db *sql.DB,
	t auditRetentionTable,
	workspaceID string,
	retentionDays int,
) (deleted int, capped bool, err error) {
	if db == nil {
		return 0, false, errors.New("sweep " + t.table + ": db is nil")
	}
	if retentionDays <= 0 {
		return 0, false, nil
	}

	// tsformat is fixed-width and lexicographically sortable, matching how
	// pipeline retention builds its cutoff. Both audit tables store
	// second-precision RFC 3339 written by Go, while their column DEFAULTs
	// render milliseconds — so a row can sit on either side of the cutoff by
	// under a second. Irrelevant at day granularity, and the direction is
	// conservative: a row exactly on the boundary is kept, not deleted.
	cutoff := tsformat.Format(time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UTC())

	scope := "workspace_id = ?"
	args := []any{workspaceID}
	if workspaceID == "" && t.wsColNullable {
		// Rows whose workspace could not be attributed — a backfill that
		// found no parent credential. They belong to no tenant, so no
		// per-workspace pass would ever reach them; without this they would
		// live forever, which is precisely the growth this file exists to
		// stop.
		scope = "workspace_id IS NULL"
		args = nil
	}

	//nolint:gosec // table and column names are package constants, never input.
	stmt := fmt.Sprintf(
		`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s AND %s < ? LIMIT ?)`,
		t.table, t.table, scope, t.tsCol)

	for i := 0; i < auditRetentionMaxBatches; i++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return deleted, false, ctxErr
		}
		batchArgs := append(append([]any{}, args...), cutoff, auditRetentionBatchRows)
		res, execErr := db.ExecContext(ctx, stmt, batchArgs...)
		if execErr != nil {
			return deleted, false, fmt.Errorf("sweep %s: %w", t.table, execErr)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
		if n < auditRetentionBatchRows {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

// SweepAllWorkspacesAuditRetention runs both audit sweeps against every
// workspace using that workspace's configured window, then sweeps the
// unattributed credential_audit rows.
//
// Errors are accumulated rather than returned on the first failure, so one
// bad workspace does not stop the sweep for the rest — same as
// pipeline.SweepAllWorkspacesRunRetention.
func SweepAllWorkspacesAuditRetention(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if db == nil {
		return errors.New("sweep audit retention: db is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}

	rows, err := db.QueryContext(ctx,
		`SELECT id, credential_audit_retention_days, audit_log_retention_days FROM workspaces`)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	type wsWindows struct {
		id            string
		credDays      int
		auditLogDays  int
		auditExplicit bool
	}
	var entries []wsWindows
	for rows.Next() {
		var id string
		var credDays, logDays sql.NullInt64
		if scanErr := rows.Scan(&id, &credDays, &logDays); scanErr != nil {
			rows.Close()
			return fmt.Errorf("scan workspace row: %w", scanErr)
		}
		// NULL and 0 mean DIFFERENT things here, and the distinction is
		// deliberate:
		//
		//   NULL → no opinion recorded; use the product default.
		//   0    → the operator's explicit "keep forever".
		//
		// This diverges from workspaces.run_retention_days, where "NULL or
		// <= 0" both fall back to the default. That is fine for pipeline
		// runs, which nobody has a legal duty to retain — but these are audit
		// tables, and "keep this forever" is a retention decision an operator
		// must be able to express. Collapsing 0 into the default would make
		// credential_audit pruning impossible to switch off, which is not a
		// choice we get to make on a self-hosted product.
		e := wsWindows{
			id:           id,
			credDays:     DefaultCredentialAuditRetentionDays,
			auditLogDays: DefaultAuditLogRetentionDays,
		}
		if credDays.Valid {
			e.credDays = int(credDays.Int64)
		}
		if logDays.Valid {
			e.auditLogDays = int(logDays.Int64)
			e.auditExplicit = true
		}
		entries = append(entries, e)
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate workspaces: %w", rowsErr)
	}

	var errs []error
	for _, e := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		for _, job := range []struct {
			t    auditRetentionTable
			days int
		}{
			{credentialAuditRetention, e.credDays},
			{auditLogRetention, e.auditLogDays},
		} {
			n, capped, sweepErr := sweepAuditTable(ctx, db, job.t, e.id, job.days)
			if sweepErr != nil {
				errs = append(errs, fmt.Errorf("workspace %s: %w", e.id, sweepErr))
				continue
			}
			if n > 0 {
				logger.Info("audit retention sweep",
					"table", job.t.table, "workspace_id", e.id,
					"deleted", n, "retention_days", job.days)
			}
			if capped {
				logger.Warn("audit retention sweep stopped at its batch cap",
					"table", job.t.table, "workspace_id", e.id,
					"deleted", n, "max_batches", auditRetentionMaxBatches,
					"note", "backlog outran one tick; it resumes on the next sweep")
			}
		}

		if !e.auditExplicit {
			warnIfAuditLogUnbounded(ctx, db, logger, e.id)
		}
	}

	// Unattributed credential_audit rows belong to no workspace, so the loop
	// above never reached them.
	if n, capped, orphanErr := sweepAuditTable(ctx, db, credentialAuditRetention, "", DefaultCredentialAuditRetentionDays); orphanErr != nil {
		errs = append(errs, fmt.Errorf("unattributed credential_audit rows: %w", orphanErr))
	} else if n > 0 {
		logger.Info("audit retention sweep: unattributed credential_audit rows",
			"deleted", n, "capped", capped)
	}

	return errors.Join(errs...)
}

// warnIfAuditLogUnbounded surfaces the consequence of the deliberate
// keep-forever default. An operator who never set a window should learn that
// the table is large from a log line, not from a full disk.
func warnIfAuditLogUnbounded(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_logs WHERE workspace_id = ?`, workspaceID).Scan(&n); err != nil {
		return // best effort; a failed COUNT must not fail the sweep
	}
	if n < auditLogUnboundedWarnRows {
		return
	}
	logger.Warn("audit_logs has no retention window and is large",
		"workspace_id", workspaceID, "rows", n,
		"note", "audit_logs is kept forever by default because retention obligations are the operator's to set; "+
			"set workspaces.audit_log_retention_days to opt this workspace into pruning")
}

// StartAuditRetentionSweeper runs SweepAllWorkspacesAuditRetention every
// `interval`, with an immediate first sweep so a freshly started server does
// not wait a full day. Blocks; callers run it as a goroutine.
//
// Not leader-gated, matching pipeline.StartRunRetentionSweeper and the ~20
// other background writers in this codebase. On a multi-replica deployment
// every replica would sweep, which is wasteful but not incorrect — the DELETE
// is idempotent and the loser simply deletes zero rows. Gating the whole
// sweeper family on the leader lease is tracked separately (#1891).
func StartAuditRetentionSweeper(ctx context.Context, db *sql.DB, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := SweepAllWorkspacesAuditRetention(ctx, db, logger); err != nil {
		logger.Warn("audit retention sweeper: initial sweep failed", "error", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := SweepAllWorkspacesAuditRetention(ctx, db, logger); err != nil {
				logger.Warn("audit retention sweeper: tick failed", "error", err)
			}
		}
	}
}
