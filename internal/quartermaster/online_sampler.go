package quartermaster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// OnlineSampler is the ADLC phase-7 worker that watches completed
// pipeline runs and queues a configurable percentage of them for
// rubric grading via the existing eval_runs table. It runs on a
// fixed interval (no cron expression — sampling cadence is internal
// implementation, not user-facing config) and is safe to start in
// duplicate accidentally: the queue insert idempotency key dedups
// at the DB level.
//
// Design notes:
//
//  1. Per-routine sample_rate lives on the routine DSL
//     (pipeline.OnlineEvalConfig). The sampler reads the active DSL
//     for each completed run when deciding whether to enqueue.
//
//  2. Cryptographic randomness (crypto/rand) over math/rand so an
//     adversary who knows the deployment's PRNG seed can't predict
//     which runs will be graded and time their attacks. The cost
//     differential is negligible — one rand draw per completed run
//     poll, batched at the work-stealing pace.
//
//  3. The sampler doesn't actually grade — it queues. Grading is the
//     existing Judge pipeline's job; we INSERT a row with status =
//     'queued' and let the judge worker pick it up. Decoupling
//     means a slow grader doesn't back-pressure the poll loop and
//     vice versa.
//
//  4. The pipeline_runs.completed_at >= last_scan_at pattern is the
//     watermark: each tick scans rows that completed since the
//     previous tick. On crash recovery the watermark resets to
//     (now - 1h) so we don't re-sample the entire backlog but also
//     don't miss the runs that completed during the outage.
type OnlineSampler struct {
	db       *sql.DB
	emitter  journal.Emitter
	logger   *slog.Logger
	interval time.Duration
	// dslResolver lets the sampler ask "what's the current DSL for
	// pipeline X?" without importing the executor. The store package
	// already implements this; tests pass a fake.
	dslResolver DSLResolver

	// watermark is the start of the next scan window; carrying state
	// here keeps the sampler self-contained instead of requiring a
	// dedicated DB column for "last_online_eval_scan_at".
	watermark time.Time
}

// DSLResolver returns the active DSL for a pipeline by id. Implemented
// by pipeline.Store in production; a tiny fake suffices for tests.
type DSLResolver interface {
	GetDSLByPipelineID(ctx context.Context, pipelineID string) (*pipeline.DSL, error)
}

// SamplerConfig groups the construction parameters. Interval is the
// poll cadence; default of 1 minute matches the ADLC drift-detection
// recommendations without inflating DB load on small deployments.
type SamplerConfig struct {
	DB          *sql.DB
	Emitter     journal.Emitter
	Logger      *slog.Logger
	Interval    time.Duration
	DSLResolver DSLResolver
}

// NewOnlineSampler constructs a sampler. Returns nil + error when
// required dependencies are missing so the caller doesn't end up
// running a no-op worker that swallows its own misconfiguration.
func NewOnlineSampler(cfg SamplerConfig) (*OnlineSampler, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("online sampler: DB required")
	}
	if cfg.DSLResolver == nil {
		return nil, fmt.Errorf("online sampler: DSLResolver required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	return &OnlineSampler{
		db:          cfg.DB,
		emitter:     cfg.Emitter,
		logger:      logger,
		interval:    interval,
		dslResolver: cfg.DSLResolver,
		watermark:   time.Now().Add(-1 * time.Hour).UTC(),
	}, nil
}

// Start kicks off the poll loop. Returns when ctx is cancelled.
// Errors during a tick are logged but never abort the loop — a
// flaky DB connection shouldn't kill continuous grading until the
// process is restarted.
func (s *OnlineSampler) Start(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	// Run one tick immediately so a startup picks up backlog without
	// waiting the full interval. Subsequent ticks come from the ticker.
	s.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce(ctx)
		}
	}
}

// scannedRun is the candidate row carried from the SELECT to the
// per-row decision loop. We buffer rows into a slice before iterating
// so the SELECT cursor is closed before we issue any INSERTs — without
// that, a single-connection pool (the test setup, plus any future
// production deployment that pins MaxOpenConns for SQLite serialization)
// would deadlock between the open Rows cursor and the INSERT trying
// to acquire the same connection.
type scannedRun struct {
	id          string
	workspaceID string
	pipelineID  string
	slug        string
}

// runOnce executes a single scan + enqueue pass. Exported via tests
// as a side effect of the Tick helper; in production it only runs
// from the loop above.
func (s *OnlineSampler) runOnce(ctx context.Context) {
	scanEnd := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workspace_id, pipeline_id, pipeline_slug
FROM pipeline_runs
WHERE status = 'completed'
  AND completed_at IS NOT NULL
  AND completed_at >= ?
  AND completed_at < ?
ORDER BY completed_at ASC
LIMIT 500
`, s.watermark.Format(time.RFC3339Nano), scanEnd.Format(time.RFC3339Nano))
	if err != nil {
		s.logger.Warn("online sampler scan failed", "err", err)
		return
	}

	candidates := make([]scannedRun, 0, 32)
	for rows.Next() {
		var r scannedRun
		if err := rows.Scan(&r.id, &r.workspaceID, &r.pipelineID, &r.slug); err != nil {
			s.logger.Warn("online sampler scan row failed", "err", err)
			continue
		}
		candidates = append(candidates, r)
	}
	iterErr := rows.Err()
	rows.Close()
	if iterErr != nil {
		s.logger.Warn("online sampler scan iter failed", "err", iterErr)
		return
	}

	enqueued := 0
	for _, r := range candidates {
		dsl, err := s.dslResolver.GetDSLByPipelineID(ctx, r.pipelineID)
		if err != nil || dsl == nil || dsl.Eval == nil || dsl.Eval.Online == nil {
			continue
		}
		rate := dsl.Eval.Online.SampleRate
		if rate <= 0 {
			continue
		}
		if rate < 1.0 && cryptoSample() >= rate {
			continue
		}

		if err := s.enqueue(ctx, r.workspaceID, r.slug, r.id, dsl.Eval.Online); err != nil {
			s.logger.Warn("online sampler enqueue failed",
				"err", err, "run_id", r.id, "pipeline_id", r.pipelineID)
			continue
		}
		enqueued++
	}

	// Advance watermark only after a clean scan — a partial scan that
	// errored mid-iteration leaves the watermark alone so the next
	// tick re-covers the same window.
	s.watermark = scanEnd

	if len(candidates) > 0 {
		s.logger.Debug("online sampler tick",
			"scanned", len(candidates), "enqueued", enqueued,
			"watermark", s.watermark.Format(time.RFC3339))
	}
}

// enqueue inserts a queued row into eval_runs. The grader worker
// (existing infrastructure) picks it up on its own poll and writes
// the result back. We don't block on grading here — the sampler is
// fire-and-forget.
func (s *OnlineSampler) enqueue(
	ctx context.Context,
	workspaceID, routineSlug, pipelineRunID string,
	cfg *pipeline.OnlineEvalConfig,
) error {
	id := generateRunID()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO eval_runs
    (id, workspace_id, kind, status, routine_slug, pipeline_run_id, sample_rate)
VALUES (?, ?, 'online', 'queued', ?, ?, ?)
`, id, workspaceID, routineSlug, pipelineRunID, cfg.SampleRate)
	if err != nil {
		return err
	}
	if s.emitter != nil {
		_, _ = s.emitter.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			Type:        journal.EntryEvalRunStarted,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorSystem,
			Summary:     fmt.Sprintf("online eval queued: %s (rate=%.3f)", routineSlug, cfg.SampleRate),
			Payload: map[string]any{
				"eval_run_id":     id,
				"kind":            "online",
				"routine_slug":    routineSlug,
				"pipeline_run_id": pipelineRunID,
				"sample_rate":     cfg.SampleRate,
				"grader_agent":    cfg.GraderAgentSlug,
			},
		})
	}
	return nil
}

// cryptoSample returns a float in [0,1) drawn from crypto/rand. Used
// instead of math/rand so the sampling pattern can't be predicted
// from a known PRNG seed.
func cryptoSample() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback to a deterministic mid-range value when the OS
		// entropy source is unavailable. This is conservative — we
		// over-sample on entropy outages, which is the safer side
		// for an observability feature.
		return 0.5
	}
	// Mask to 53 bits (float64 mantissa) and divide for uniform [0,1).
	u := binary.BigEndian.Uint64(b[:]) & ((1 << 53) - 1)
	return float64(u) / (1 << 53)
}

// generateRunID mints a fresh eval_runs.id. Local to this file to
// avoid importing the API-layer cuid generator; eval_runs.id is opaque
// and only needs to be unique per row.
func generateRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("eval_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("eval_%x", b[:])
}
