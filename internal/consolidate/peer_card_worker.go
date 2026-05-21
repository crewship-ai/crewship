package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// PR-E F6 — PeerCardSync server-bootstrap integration.
//
// The RunPeerCardSync primitive defined in peer_card_routine.go is
// workspace-scoped. The server has many workspaces, and we want a
// single daily sweep that walks all of them. StartPeerCardSyncWorker
// is the long-running goroutine wired from cmd/crewship/cmd_start.go
// (next to StartRegistrySyncWorker / StartOAuthRefreshWorker) that
//
//   1. queries the active workspaces,
//   2. calls RunPeerCardSync once per workspace,
//   3. aggregates summaries for the daily log line,
//   4. waits 24h and repeats.
//
// Cadence: 24h ticker. Offset is "wall-clock 04:00 UTC of the next
// day" computed at start so a Crewship instance booted at 09:00 won't
// fire its first sweep until 04:00 next day instead of 19h later
// (which would otherwise pile on top of EphemeralExpiry once that
// lands at e.g. 03:00 UTC).
//
// Extractor: NoopExtractor by default. PR-F will wire the aux-LLM-
// driven extractor through the aux model slot from PR-B F3; until
// then, the routine still does useful work (purges opt-outs, indexes
// threshold-crossers with empty bodies). Callers that want a real
// extractor can pass one via PeerCardWorkerConfig.Extractor.
//
// Pattern mirrors api.StartRegistrySyncWorker — explicit stop chan +
// WaitGroup so cmd_start.go can deterministically tear down on
// SIGTERM. No leader election; if the operator runs multiple
// replicas they will double-fire (same caveat as the MCP registry
// worker and the pipeline scheduler).

// PeerCardWorkerConfig parameterises the background worker.
type PeerCardWorkerConfig struct {
	// BasePath is the host-side bind-mount root. Must equal
	// cfg.Storage.BasePath in production; tests pass a t.TempDir.
	BasePath string
	// Extractor produces peer card bodies. Defaults to NoopExtractor
	// (purge + index, no content). PR-F wires an aux-LLM-driven
	// extractor here.
	Extractor PeerExtractor
	// Threshold overrides the per-pair eligibility threshold. Zero
	// value falls back to DefaultPeerCardThreshold inside RunPeerCardSync.
	Threshold PeerCardThreshold
	// LookbackWindow bounds the chats query. Zero falls back to 14d.
	LookbackWindow time.Duration
	// TickInterval defaults to 24h. Override for tests.
	TickInterval time.Duration
	// FirstRunDelay is how long to wait after Start before the first
	// sweep. Defaults to nextLocalOffset(04:00 UTC) so initial fires
	// happen during off-peak. Override for tests (use a small value
	// to trigger the first sweep immediately).
	FirstRunDelay time.Duration
}

// StartPeerCardSyncWorker launches the background goroutine and
// returns immediately. The goroutine exits when stop is closed; wg
// tracks lifecycle for graceful shutdown.
//
// Errors from individual workspaces are logged but do not abort the
// loop — a single corrupted workspace must not silently halt the
// daily sweep for every other tenant on the instance.
func StartPeerCardSyncWorker(
	db *sql.DB,
	logger *slog.Logger,
	cfg PeerCardWorkerConfig,
	stop <-chan struct{},
	wg *sync.WaitGroup,
) {
	if cfg.BasePath == "" {
		// No bind-mount root means SyncPeerCard would fail every call;
		// fail loudly at boot rather than emitting one warning per tick.
		logger.Error("peer card sync worker not started: BasePath required")
		return
	}
	if cfg.Extractor == nil {
		cfg.Extractor = NoopExtractor{}
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 24 * time.Hour
	}
	if cfg.FirstRunDelay < 0 {
		cfg.FirstRunDelay = 0
	}
	if cfg.FirstRunDelay == 0 {
		cfg.FirstRunDelay = nextDailyOffsetUTC(time.Now().UTC(), 4)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()

		logger.Info("peer card sync worker started",
			"first_run_in", cfg.FirstRunDelay.Round(time.Second),
			"tick_interval", cfg.TickInterval)

		// First run: scheduled to land at the configured wall-clock
		// offset. Skip if the operator shuts down during the delay.
		select {
		case <-stop:
			return
		case <-time.After(cfg.FirstRunDelay):
		}
		runSweepAllWorkspaces(ctx, db, logger, cfg)

		ticker := time.NewTicker(cfg.TickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				runSweepAllWorkspaces(ctx, db, logger, cfg)
			}
		}
	}()
}

// runSweepAllWorkspaces enumerates active workspaces and runs the
// per-workspace routine on each. Aggregates a single summary log
// line so an operator can grep for `peer card sync sweep complete`
// and see total work done by the daily run.
func runSweepAllWorkspaces(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	cfg PeerCardWorkerConfig,
) {
	workspaces, err := loadActiveWorkspaceIDs(ctx, db)
	if err != nil {
		logger.Error("peer card sync: load workspaces failed", "err", err)
		return
	}
	if len(workspaces) == 0 {
		logger.Debug("peer card sync: no active workspaces")
		return
	}
	started := time.Now()
	var totals PeerSyncSummary
	for _, wsID := range workspaces {
		// Respect cancellation between workspaces so SIGTERM doesn't
		// have to wait for every workspace's sweep to finish before
		// the goroutine returns.
		if ctx.Err() != nil {
			return
		}
		sum, err := RunPeerCardSync(ctx, db, logger, wsID, PeerCardSyncOptions{
			OutputBasePath: cfg.BasePath,
			Threshold:      cfg.Threshold,
			Extractor:      cfg.Extractor,
			LookbackWindow: cfg.LookbackWindow,
		})
		if err != nil {
			logger.Warn("peer card sync: workspace sweep failed",
				"workspace_id", wsID, "err", err)
			continue
		}
		totals.Candidates += sum.Candidates
		totals.Writes += sum.Writes
		totals.SkippedThresh += sum.SkippedThresh
		totals.SkippedEmpty += sum.SkippedEmpty
		totals.PurgedOptOut += sum.PurgedOptOut
		totals.Errors += sum.Errors
	}
	logger.Info("peer card sync sweep complete",
		"workspaces", len(workspaces),
		"candidates", totals.Candidates,
		"writes", totals.Writes,
		"skipped_threshold", totals.SkippedThresh,
		"skipped_empty", totals.SkippedEmpty,
		"purged_opt_out", totals.PurgedOptOut,
		"errors", totals.Errors,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

// loadActiveWorkspaceIDs returns every non-deleted workspace. Cheap
// query — workspaces table is tiny relative to chats and is fully
// indexed. The deleted_at filter matches the convention used across
// the rest of the codebase (soft delete semantics from CLAUDE.md).
func loadActiveWorkspaceIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id FROM workspaces WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("query workspaces: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan workspace id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// nextDailyOffsetUTC returns the duration from `now` until the next
// wall-clock occurrence of `hourUTC:00:00` UTC. If `now` is already
// past that hour today, the result is "tomorrow at hourUTC". Used to
// align the first sweep with off-peak hours regardless of process
// start time, so EphemeralExpiry (planned for ~03:00) and PeerCardSync
// (~04:00) don't collide.
func nextDailyOffsetUTC(now time.Time, hourUTC int) time.Duration {
	if hourUTC < 0 || hourUTC > 23 {
		hourUTC = 4
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), hourUTC, 0, 0, 0, time.UTC)
	if !target.After(now) {
		target = target.Add(24 * time.Hour)
	}
	return target.Sub(now)
}
