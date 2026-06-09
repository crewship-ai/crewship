package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR #10 F6 — UserModelSync daily routine + background worker.
//
// Mirrors peer_card_routine.go + peer_card_worker.go but the unit of
// identity is (user, workspace), not (agent, user). One sweep walks
// every operator who crossed the interaction threshold in a workspace,
// loads their prior on-disk model, asks the extractor to refresh it
// (MERGE-preserving silent fields), and persists via SyncUserModel.
//
// The model is crew-shared: it lands under the operator's most-active
// crew's shared memory (/crew/shared/.memory/users/{slug}.md) so every
// agent in that crew reads the same picture of the operator.
//
// Reuses package helpers from the peer card subsystem:
//   - loadActiveWorkspaceIDs (peer_card_worker.go)
//   - nextDailyOffsetUTC      (peer_card_worker.go)
//   - parseSessionDuration    (peer_card_routine.go)
//   - IsOptedOut / recordAudit (peer_card_writer.go) via SyncUserModel

// UserModelExtractor produces the refreshed markdown body for a user
// model given the candidate signal AND the prior on-disk model so it
// can merge rather than overwrite. Implementations may call the aux
// LLM slot, a deterministic summariser, or no-op.
//
// Return ("", nil) when there's nothing meaningful to refresh —
// SyncUserModel treats empty content as skip_empty_content rather than
// a write of empty bytes.
type UserModelExtractor interface {
	Extract(ctx context.Context, cand UserModelCandidate, prior string) (string, error)
}

// NoopUserModelExtractor is the MVP placeholder: returns empty content
// so the sweep purges opted-out users + indexes threshold-crossers
// without writing new bodies. Wire a real aux-LLM-driven extractor via
// UserModelWorkerConfig.Extractor when ready.
type NoopUserModelExtractor struct{}

func (NoopUserModelExtractor) Extract(_ context.Context, _ UserModelCandidate, _ string) (string, error) {
	return "", nil
}

// UserModelSyncOptions parameterises the routine. OutputBasePath must
// match the host-side cfg.Storage.BasePath the container provider uses
// for bind mounts.
type UserModelSyncOptions struct {
	OutputBasePath string
	Threshold      UserModelThreshold
	Extractor      UserModelExtractor
	// LookbackWindow bounds the chats query. Defaults to 14 days.
	LookbackWindow time.Duration
}

// UserModelSyncSummary is the per-outcome counter a routine run hands
// back for journal emission + metrics.
type UserModelSyncSummary struct {
	WorkspaceID   string
	Candidates    int
	Writes        int
	SkippedThresh int
	SkippedEmpty  int
	SkippedOptOut int
	PurgedOptOut  int
	Errors        int
}

// RunUserModelSync walks every active operator in the workspace that
// meets the interaction threshold and either writes a refreshed,
// merged model (consent != opted_out) or purges an existing one
// (opt-out path). Per-candidate failures are logged, not returned, so
// one bad operator doesn't poison the sweep.
func RunUserModelSync(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	workspaceID string,
	opts UserModelSyncOptions,
) (UserModelSyncSummary, error) {
	if opts.Extractor == nil {
		opts.Extractor = NoopUserModelExtractor{}
	}
	if opts.Threshold == (UserModelThreshold{}) {
		opts.Threshold = DefaultUserModelThreshold
	}
	if opts.LookbackWindow <= 0 {
		opts.LookbackWindow = 14 * 24 * time.Hour
	}
	if opts.OutputBasePath == "" {
		return UserModelSyncSummary{WorkspaceID: workspaceID},
			fmt.Errorf("user model routine: OutputBasePath required")
	}

	cands, err := loadUserModelCandidates(ctx, db, workspaceID, opts.LookbackWindow)
	if err != nil {
		return UserModelSyncSummary{WorkspaceID: workspaceID},
			fmt.Errorf("run user model sync: load candidates for workspace %s: %w", workspaceID, err)
	}
	sum := UserModelSyncSummary{WorkspaceID: workspaceID, Candidates: len(cands)}
	now := time.Now()
	for _, cand := range cands {
		paths := userModelPathsFor(opts.OutputBasePath, cand.CrewID)
		prior, _ := memory.LoadUserModel(paths, cand.UserID, cand.WorkspaceID)
		extracted, eerr := opts.Extractor.Extract(ctx, cand, prior)
		if eerr != nil {
			logger.Warn("user model extractor failed",
				"user_id", cand.UserID, "workspace_id", cand.WorkspaceID, "err", eerr)
			sum.Errors++
			continue
		}
		// MERGE: fold the freshly-extracted fields over the prior model
		// so silent prior fields survive. Empty extraction → prior is
		// returned unchanged, which then re-writes the same content
		// (idempotent) unless it too is empty.
		content := MergeUserModel(prior, extracted)
		out := SyncUserModel(ctx, db, logger, opts.Threshold, cand, content, paths, now)
		if out.Err != nil {
			sum.Errors++
			logger.Warn("user model sync candidate failed",
				"user_id", cand.UserID, "action", out.Action, "err", out.Err)
			continue
		}
		switch out.Action {
		case "write":
			sum.Writes++
		case "skip_threshold":
			sum.SkippedThresh++
		case "skip_empty_content":
			sum.SkippedEmpty++
		case "skip_opt_out":
			sum.SkippedOptOut++
		case "delete_opt_out":
			sum.PurgedOptOut++
		}
	}
	return sum, nil
}

// userModelPathsFor resolves the crew-shared memory directory that
// holds a user model. crewID may be empty (a workspace with no crew on
// the chat path); in that case the model lands in a workspace-level
// fallback shared dir so the file still has a home.
func userModelPathsFor(basePath, crewID string) memory.UserModelPaths {
	if crewID == "" {
		return memory.UserModelPaths{
			SharedDir: filepath.Join(basePath, "workspace", "shared", ".memory"),
		}
	}
	return memory.UserModelPaths{
		SharedDir: filepath.Join(basePath, "crews", crewID, "shared", ".memory"),
	}
}

// loadUserModelCandidates aggregates per-operator interaction signal
// across the workspace. Unlike the peer candidate query (grouped by
// agent + user), this groups by USER alone — the model is workspace-
// scoped. The crew_id picked is the user's most-active crew (highest
// summed message_count), which decides where the crew-shared file
// lands.
func loadUserModelCandidates(ctx context.Context, db *sql.DB, workspaceID string, lookback time.Duration) ([]UserModelCandidate, error) {
	since := time.Now().UTC().Add(-lookback).Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT
		    c.created_by                                AS user_id,
		    SUM(COALESCE(c.message_count, 0))           AS message_count,
		    MIN(c.started_at)                           AS first_seen,
		    MAX(COALESCE(c.ended_at, c.started_at))     AS last_seen,
		    (
		        SELECT a2.crew_id
		        FROM chats c2
		        JOIN agents a2 ON a2.id = c2.agent_id
		        WHERE c2.workspace_id = c.workspace_id
		          AND c2.created_by = c.created_by
		          AND c2.started_at >= ?
		          AND a2.deleted_at IS NULL
		          AND a2.crew_id IS NOT NULL
		        GROUP BY a2.crew_id
		        ORDER BY SUM(COALESCE(c2.message_count, 0)) DESC
		        LIMIT 1
		    )                                           AS crew_id
		FROM chats c
		JOIN agents a ON a.id = c.agent_id
		WHERE c.workspace_id = ?
		  AND c.created_by IS NOT NULL
		  AND c.started_at >= ?
		  AND a.deleted_at IS NULL
		GROUP BY c.created_by
	`, since, workspaceID, since)
	if err != nil {
		return nil, fmt.Errorf("user model candidate query: %w", err)
	}
	defer rows.Close()
	var out []UserModelCandidate
	for rows.Next() {
		var (
			userID              string
			msgCount            int
			firstSeen, lastSeen string
			crewID              sql.NullString
		)
		if err := rows.Scan(&userID, &msgCount, &firstSeen, &lastSeen, &crewID); err != nil {
			return nil, fmt.Errorf("scan user model candidate (workspace %s): %w", workspaceID, err)
		}
		out = append(out, UserModelCandidate{
			WorkspaceID:     workspaceID,
			CrewID:          crewID.String,
			UserID:          userID,
			MessageCount:    msgCount,
			SessionDuration: parseSessionDuration(firstSeen, lastSeen),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user model candidates (workspace %s): %w", workspaceID, err)
	}
	return out, nil
}

// UserModelWorkerConfig parameterises the background worker. Mirrors
// PeerCardWorkerConfig.
type UserModelWorkerConfig struct {
	BasePath       string
	Extractor      UserModelExtractor
	Threshold      UserModelThreshold
	LookbackWindow time.Duration
	TickInterval   time.Duration
	FirstRunDelay  time.Duration
}

// StartUserModelSyncWorker launches the daily sweep goroutine and
// returns immediately. Exits when stop is closed; wg tracks lifecycle.
// Errors from individual workspaces are logged, not fatal.
func StartUserModelSyncWorker(
	db *sql.DB,
	logger *slog.Logger,
	cfg UserModelWorkerConfig,
	stop <-chan struct{},
	wg *sync.WaitGroup,
) {
	if cfg.BasePath == "" {
		logger.Error("user model sync worker not started: BasePath required")
		return
	}
	if cfg.Extractor == nil {
		cfg.Extractor = NoopUserModelExtractor{}
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 24 * time.Hour
	}
	if cfg.FirstRunDelay < 0 {
		cfg.FirstRunDelay = 0
	}
	if cfg.FirstRunDelay == 0 {
		// 05:00 UTC — one hour after PeerCardSync (04:00) so the two
		// daily memory sweeps don't pile onto the same minute.
		cfg.FirstRunDelay = nextDailyOffsetUTC(time.Now().UTC(), 5)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()

		logger.Info("user model sync worker started",
			"first_run_in", cfg.FirstRunDelay.Round(time.Second),
			"tick_interval", cfg.TickInterval)

		select {
		case <-stop:
			return
		case <-time.After(cfg.FirstRunDelay):
		}
		runUserModelSweepAll(ctx, db, logger, cfg)

		ticker := time.NewTicker(cfg.TickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				runUserModelSweepAll(ctx, db, logger, cfg)
			}
		}
	}()
}

// runUserModelSweepAll enumerates active workspaces and runs the
// per-workspace routine on each, aggregating one summary log line.
func runUserModelSweepAll(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	cfg UserModelWorkerConfig,
) {
	workspaces, err := loadActiveWorkspaceIDs(ctx, db)
	if err != nil {
		logger.Error("user model sync: load workspaces failed", "err", err)
		return
	}
	if len(workspaces) == 0 {
		logger.Debug("user model sync: no active workspaces")
		return
	}
	started := time.Now()
	var totals UserModelSyncSummary
	for _, wsID := range workspaces {
		if ctx.Err() != nil {
			return
		}
		sum, err := RunUserModelSync(ctx, db, logger, wsID, UserModelSyncOptions{
			OutputBasePath: cfg.BasePath,
			Threshold:      cfg.Threshold,
			Extractor:      cfg.Extractor,
			LookbackWindow: cfg.LookbackWindow,
		})
		if err != nil {
			logger.Warn("user model sync: workspace sweep failed",
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
	logger.Info("user model sync sweep complete",
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
