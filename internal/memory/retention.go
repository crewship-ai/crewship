package memory

// Per-workspace retention sweep for the memory_versions audit trail.
//
// Background. Every successful memory.WriteFile call appends a row to
// memory_versions plus a content-addressed blob on disk. Without a
// retention sweep the row count grows monotonically — bounded only by
// dedupe on the blob side. The PRD MEMORY-ROADMAP-2026 specifies a
// 30-day default trimmed daily; this file implements the per-workspace
// override path that pulls the cutoff from
// workspaces.memory_config.versions_retention_days so different
// workspaces can keep history for different windows (a compliance-
// audited workspace might keep 365 days, a dev sandbox 7).
//
// What this file does NOT do.
//
//   - Delete on-disk blobs. The payload_ref column points at a content-
//     addressed blob that's deduped across rows — a row delete does
//     not imply a blob delete because another row may still reference
//     the same sha. Orphan-blob GC is the existing
//     consolidate.runCompactionLoop's job (it walks the blob root and
//     deletes any sha not referenced by any remaining row). A follow-
//     up PR can move that blob-sweep call to fire after every
//     SweepAllWorkspaces tick if operators want tighter coupling, but
//     the current split keeps each tick's responsibility narrow.
//
//   - Honor the "keep latest N per path" floor. The existing
//     memory.PruneOldVersions still implements that rule for the
//     global daily sweep. The per-workspace path documented in the
//     PRD is the cutoff-only flavour — operators wanting the keep-N
//     floor route through PruneOldVersions instead. A follow-up can
//     unify if dual-paths prove confusing in operator dashboards.
//
// Crash-safety. Every sweep is one DELETE per workspace with a
// concrete cutoff. SQLite is transactional per statement; a partial
// run (process killed mid-tick) leaves rows either fully deleted or
// fully present — never half-deleted. The next tick re-runs the same
// query and is naturally idempotent.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// DefaultRetentionDays is the cutoff applied when a workspace has
// no memory_config row, has memory_config = NULL, or its JSON does
// not contain a versions_retention_days key. Matches the PRD spec
// (MEMORY-ROADMAP-2026 §6) and Anthropic Managed Agents' shipped
// default.
const DefaultRetentionDays = 30

// SweepStaleVersions deletes memory_versions rows for one workspace
// whose written_at is older than (now - retentionDays). Returns the
// deleted row count.
//
// Behaviour contract:
//
//   - workspaceID must be non-empty. An empty value returns an error
//     rather than running a tenant-blind DELETE — defence-in-depth
//     against a future caller that forgets to validate input.
//
//   - retentionDays <= 0 disables the sweep for this workspace (returns
//     0, nil). This is the documented "opt out" for ops who want to
//     keep history indefinitely.
//
//   - When deletedCount > 0 a journal event EntryMemoryVersionsSwept
//     is emitted with workspace_id, deleted_count, retention_days.
//     The emit happens AFTER the delete commits, so an interrupted
//     sweep that did delete rows but crashed before journaling still
//     leaves the DB in a consistent state — operators just miss the
//     event for that one tick. A journal-emit failure is logged but
//     does not promote to a top-level error: the rows are already
//     gone and re-running the sweep would not regenerate the journal
//     entry. Caller has the deletedCount as the authoritative result.
//
//   - Idempotent: re-running with the same arguments after a successful
//     sweep deletes 0 rows (everything older than the cutoff is gone)
//     and emits no journal event.
func SweepStaleVersions(
	ctx context.Context,
	db *sql.DB,
	emitter journal.Emitter,
	workspaceID string,
	retentionDays int,
) (int, error) {
	if db == nil {
		return 0, errors.New("sweep memory_versions: db is nil")
	}
	if workspaceID == "" {
		return 0, errors.New("sweep memory_versions: workspace_id required")
	}
	if retentionDays <= 0 {
		return 0, nil
	}

	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).
		UTC().Format(time.RFC3339Nano)

	// Single statement, tenant-scoped. SQLite implicitly wraps this in
	// a transaction so a crash mid-DELETE rolls back to the pre-state;
	// the next tick re-applies cleanly. The query is the slow path
	// (written_at is not the primary key) but the idx_memory_versions_
	// ws_path_ts index covers the (workspace_id, ...) prefix.
	res, err := db.ExecContext(ctx, `
		DELETE FROM memory_versions
		 WHERE workspace_id = ? AND written_at < ?`,
		workspaceID, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("sweep memory_versions for %s: %w", workspaceID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// RowsAffected is best-effort on SQLite — surface the error
		// so the caller can log, but the DELETE itself committed so
		// don't roll anything back.
		return 0, fmt.Errorf("rows affected for %s: %w", workspaceID, err)
	}
	deleted := int(n)
	if deleted == 0 || emitter == nil {
		return deleted, nil
	}

	if _, emitErr := emitter.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryMemoryVersionsSwept,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		ActorID:     "memory_retention_sweeper",
		Summary: fmt.Sprintf(
			"swept %d memory_versions row(s) older than %d day(s)",
			deleted, retentionDays,
		),
		Payload: map[string]any{
			"workspace_id":   workspaceID,
			"deleted_count":  deleted,
			"retention_days": retentionDays,
		},
	}); emitErr != nil {
		// Journal-emit failure is operationally annoying but not
		// fatal — the rows are already gone. Log + continue.
		slog.Warn("memory retention sweep: journal emit failed",
			"workspace_id", workspaceID,
			"deleted", deleted,
			"err", emitErr,
		)
	}
	return deleted, nil
}

// SweepAllWorkspaces enumerates every workspace and runs SweepStaleVersions
// against each with that workspace's configured retention window. The
// cutoff is read from workspaces.memory_config (a JSON column added by
// the v90 migration); the key is "versions_retention_days". Missing
// column, NULL value, malformed JSON, missing key, or non-numeric value
// all fall back to DefaultRetentionDays (30) — operators do not need
// to do anything to get the default behaviour.
//
// Errors are accumulated with errors.Join so the caller sees every
// failure in one log line. A failed workspace does not stop the
// iterator — the loop continues to the next workspace so one bad row
// can't wedge the daily sweep for an entire deployment.
//
// Pass a nil emitter to disable the journal trail (test paths
// exercising the row-delete logic without the journal dependency).
func SweepAllWorkspaces(ctx context.Context, db *sql.DB, emitter journal.Emitter) error {
	if db == nil {
		return errors.New("sweep all workspaces: db is nil")
	}
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(memory_config, '') FROM workspaces`)
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
		var id, memCfg string
		if scanErr := rows.Scan(&id, &memCfg); scanErr != nil {
			return fmt.Errorf("scan workspace row: %w", scanErr)
		}
		entries = append(entries, wsEntry{
			ID:        id,
			Retention: extractRetentionDays(memCfg),
		})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate workspaces: %w", rowsErr)
	}

	var errs []error
	for _, e := range entries {
		select {
		case <-ctx.Done():
			// Respect cancellation between workspaces — shutdown
			// shouldn't wait for the entire iteration to complete.
			return ctx.Err()
		default:
		}
		if _, sweepErr := SweepStaleVersions(ctx, db, emitter, e.ID, e.Retention); sweepErr != nil {
			errs = append(errs, fmt.Errorf("workspace %s: %w", e.ID, sweepErr))
		}
	}
	return errors.Join(errs...)
}

// extractRetentionDays parses the workspaces.memory_config JSON column
// and returns versions_retention_days when present and positive.
// Anything else (empty string, malformed JSON, missing key, zero,
// negative, non-numeric) falls back to DefaultRetentionDays. Kept
// internal because the contract is "the column is opaque JSON; this
// is the one key we care about" — exposing it widely would invite
// callers to grow the surface ad-hoc.
func extractRetentionDays(memCfg string) int {
	if memCfg == "" {
		return DefaultRetentionDays
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(memCfg), &cfg); err != nil {
		return DefaultRetentionDays
	}
	raw, ok := cfg["versions_retention_days"]
	if !ok {
		return DefaultRetentionDays
	}
	// JSON numbers unmarshal into float64 by default; ints, floats,
	// and numeric strings all need to land at a positive int. Refuse
	// negatives and zeros so the column can't accidentally disable the
	// sweep without an explicit operator decision.
	switch v := raw.(type) {
	case float64:
		if v <= 0 {
			return DefaultRetentionDays
		}
		// Positive fractional values (e.g. 0.5) would truncate to 0
		// via int(v), silently disabling the sweep — exactly the
		// "accidentally disable without an explicit decision" trap
		// the <=0 guard above tries to close. Ceil instead: any
		// positive fraction rounds up to at least 1 day. CodeRabbit
		// review catch on PR #399.
		return int(math.Ceil(v))
	case int:
		if v <= 0 {
			return DefaultRetentionDays
		}
		return v
	case int64:
		if v <= 0 {
			return DefaultRetentionDays
		}
		return int(v)
	}
	return DefaultRetentionDays
}

// StartRetentionSweeper launches a background goroutine that fires
// SweepAllWorkspaces every `interval`. Returns immediately; the
// goroutine exits when ctx is cancelled.
//
// The first sweep fires immediately (before the first ticker beat) so
// a freshly started server starts cleaning history without waiting a
// full day. Subsequent sweeps fire on the ticker. Mirrors the
// harbormaster timeout sweeper's "ticker loop with ctx cancel" shape,
// adapted to add the boot-time first tick.
//
// interval <= 0 falls back to 24h (matches the documented "daily"
// cadence in the PRD).
//
// WIRING — important. As of the per-workspace retention rollout
// (Iter 4 of the memory-hardening series), the production path
// does NOT use StartRetentionSweeper directly. The per-workspace
// pass piggy-backs the existing consolidate.runCompactionLoop
// daily tick — runCompactionLoop calls SweepAllWorkspaces after
// the global memory.PruneOldVersions pass, so one ticker drives
// both passes in coordinated order:
//
//  1. PruneOldVersions — global retention + keep-N floor + blob GC
//  2. SweepAllWorkspaces — per-workspace tightening for tenants
//     with retention_days < the global cutoff
//
// StartRetentionSweeper is kept exported for two cases the folded
// path does not cover:
//
//   - A deployment that runs ONLY the retention sweep (no
//     consolidation worker — e.g. a future "lite" mode that ships
//     without the LLM-driven consolidator) can wire this directly
//     from its own lifecycle.
//   - Tests that want to exercise the boot-time-tick / cancel
//     semantics in isolation from the consolidator's wider state
//     machine.
//
// Pointing the production server at this helper would result in
// two background loops both calling SweepAllWorkspaces — the
// folded path is the single source of truth; do not re-wire it
// from server_lifecycle.go without removing the runCompactionLoop
// call first.
func StartRetentionSweeper(
	ctx context.Context,
	db *sql.DB,
	emitter journal.Emitter,
	interval time.Duration,
) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		// Immediate first tick: operators restarting a long-lived
		// server want the sweep to fire on boot rather than waiting
		// 24h for the first cadence beat. Failure is logged and
		// the loop continues — the next tick will retry.
		if err := SweepAllWorkspaces(ctx, db, emitter); err != nil {
			slog.Warn("memory retention sweeper: initial sweep failed", "err", err)
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := SweepAllWorkspaces(ctx, db, emitter); err != nil {
					slog.Warn("memory retention sweeper: tick failed", "err", err)
				}
			}
		}
	}()
}
