package pipeline

// Per-workspace retention sweep for pipeline_runs (+ run_tags, which
// cascade-deletes via ON DELETE CASCADE — see migrate_consts_v122 — and
// warnings_json, which is a column on the row itself, not a separate
// table). Modeled directly on internal/memory/retention.go's
// SweepStaleVersions/SweepAllWorkspaces/StartRetentionSweeper shape.
//
// Without this sweep, run history on a long-lived self-hosted instance
// grows without bound — journal_entries has no bulk-delete sweep either,
// but pipeline_runs is the smaller, query-optimized projection this repo
// actually pages through, so it's the one that needs pruning first. See
// issue #1407.
//
// What this sweep protects, regardless of age:
//   - any run NOT in a terminal status (queued/running/waiting) — an
//     in-flight run is never eligible no matter how old started_at is;
//   - the most recent DefaultKeepLastNRunsPerPipeline runs per pipeline_id,
//     so a low-traffic pipeline's whole history doesn't vanish just
//     because its runs are old;
//   - any run referenced by a pending pipeline_waitpoint (an approval
//     still awaiting a decision must stay resolvable);
//   - any run that is the replay_of target of another run still present
//     in the table — deleting it would leave that other run's
//     provenance pointer dangling.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// DefaultRunRetentionDays is the cutoff applied when a workspace has no
// run_retention_days override (NULL or <= 0).
const DefaultRunRetentionDays = 90

// DefaultKeepLastNRunsPerPipeline is the always-keep floor per pipeline_id,
// independent of age or the configured retention window. Not currently
// per-workspace configurable — the window (run_retention_days) is the
// operator-facing knob; this floor exists so a low-traffic pipeline never
// loses its entire history to a strict window.
const DefaultKeepLastNRunsPerPipeline = 10

// SweepRunRetention deletes terminal pipeline_runs rows for one workspace
// older than (now - retentionDays), excluding the most recent keepLastN
// runs per pipeline_id and any run protected by a pending waitpoint or a
// surviving replay child. Returns the deleted row count.
//
// Idempotent: re-running with the same arguments after a successful sweep
// deletes 0 rows.
func SweepRunRetention(
	ctx context.Context,
	db *sql.DB,
	emitter journal.Emitter,
	workspaceID string,
	retentionDays int,
	keepLastN int,
) (int, error) {
	if db == nil {
		return 0, errors.New("sweep pipeline_runs: db is nil")
	}
	if workspaceID == "" {
		return 0, errors.New("sweep pipeline_runs: workspace_id required")
	}
	if retentionDays <= 0 {
		return 0, nil
	}
	if keepLastN < 0 {
		keepLastN = 0
	}

	// tsformat (fixed-width, lex-sortable): cutoff is compared
	// `started_at < cutoff`, and pipeline_runs.started_at is stored via
	// tsformat. A variable-width format would mis-compare across the
	// fractional-second boundary and purge the wrong rows.
	cutoff := tsformat.Format(time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UTC())

	// ranked numbers each workspace's runs 1..N per pipeline_id, newest
	// first, so "rn > keepLastN" identifies everything outside the
	// always-keep floor. The DELETE's own WHERE re-applies workspace_id,
	// terminal-status, and age so the CTE only needs to compute rank.
	res, err := db.ExecContext(ctx, `
WITH ranked AS (
    SELECT id, ROW_NUMBER() OVER (
        PARTITION BY pipeline_id ORDER BY started_at DESC
    ) AS rn
    FROM pipeline_runs
    WHERE workspace_id = ?
)
DELETE FROM pipeline_runs
WHERE workspace_id = ?
  AND status IN ('completed', 'failed', 'cancelled', 'interrupted')
  AND started_at < ?
  AND id IN (SELECT id FROM ranked WHERE rn > ?)
  AND NOT EXISTS (
      SELECT 1 FROM pipeline_waitpoints wp
      WHERE wp.pipeline_run_id = pipeline_runs.id AND wp.status = 'pending'
  )
  AND id NOT IN (
      SELECT replay_of FROM pipeline_runs WHERE replay_of IS NOT NULL
  )`,
		workspaceID, workspaceID, cutoff, keepLastN,
	)
	if err != nil {
		return 0, fmt.Errorf("sweep pipeline_runs for %s: %w", workspaceID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected for %s: %w", workspaceID, err)
	}
	deleted := int(n)
	if deleted == 0 || emitter == nil {
		return deleted, nil
	}

	if _, emitErr := emitter.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryPipelineRunsSwept,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     "run_retention_sweeper",
		Summary: fmt.Sprintf(
			"swept %d pipeline_runs row(s) older than %d day(s) (keeping last %d per pipeline)",
			deleted, retentionDays, keepLastN,
		),
		Payload: map[string]any{
			"workspace_id":             workspaceID,
			"deleted_count":            deleted,
			"retention_days":           retentionDays,
			"keep_last_n_per_pipeline": keepLastN,
		},
	}); emitErr != nil {
		// Journal-emit failure is operationally annoying but not fatal —
		// the rows are already gone. Log + continue, matching the memory
		// sweep's contract.
		slog.Warn("pipeline run retention sweep: journal emit failed",
			"workspace_id", workspaceID,
			"deleted", deleted,
			"err", emitErr,
		)
	}
	return deleted, nil
}

// SweepAllWorkspacesRunRetention enumerates every workspace and runs
// SweepRunRetention against each with that workspace's configured window
// (workspaces.run_retention_days; NULL or <= 0 falls back to
// DefaultRunRetentionDays). keepLastN is currently a single value applied
// to every workspace (DefaultKeepLastNRunsPerPipeline in production).
//
// Errors are accumulated with errors.Join so one bad workspace doesn't
// stop the sweep for the rest.
func SweepAllWorkspacesRunRetention(ctx context.Context, db *sql.DB, emitter journal.Emitter, keepLastN int) error {
	if db == nil {
		return errors.New("sweep all workspaces (runs): db is nil")
	}
	rows, err := db.QueryContext(ctx, `SELECT id, run_retention_days FROM workspaces`)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()

	type wsEntry struct {
		ID        string
		Retention int
	}
	var entries []wsEntry
	for rows.Next() {
		var id string
		var retention sql.NullInt64
		if scanErr := rows.Scan(&id, &retention); scanErr != nil {
			return fmt.Errorf("scan workspace row: %w", scanErr)
		}
		days := DefaultRunRetentionDays
		if retention.Valid && retention.Int64 > 0 {
			days = int(retention.Int64)
		}
		entries = append(entries, wsEntry{ID: id, Retention: days})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate workspaces: %w", rowsErr)
	}

	var errs []error
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if _, sweepErr := SweepRunRetention(ctx, db, emitter, e.ID, e.Retention, keepLastN); sweepErr != nil {
			errs = append(errs, fmt.Errorf("workspace %s: %w", e.ID, sweepErr))
		}
	}
	return errors.Join(errs...)
}

// StartRunRetentionSweeper launches a background goroutine that fires
// SweepAllWorkspacesRunRetention every `interval`, with an immediate first
// sweep so a freshly started server doesn't wait a full day. Mirrors
// memory.StartRetentionSweeper's shape. interval <= 0 falls back to 24h.
func StartRunRetentionSweeper(
	ctx context.Context,
	db *sql.DB,
	emitter journal.Emitter,
	interval time.Duration,
) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		if err := SweepAllWorkspacesRunRetention(ctx, db, emitter, DefaultKeepLastNRunsPerPipeline); err != nil {
			slog.Warn("pipeline run retention sweeper: initial sweep failed", "err", err)
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := SweepAllWorkspacesRunRetention(ctx, db, emitter, DefaultKeepLastNRunsPerPipeline); err != nil {
					slog.Warn("pipeline run retention sweeper: tick failed", "err", err)
				}
			}
		}
	}()
}
