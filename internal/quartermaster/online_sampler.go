package quartermaster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
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

	// mu protects watermark + watermarkID + backoffSkips. The sampler
	// is documented as a singleton (Start should run exactly once per
	// process), but operators occasionally wire it twice by accident
	// — once in the main bootstrap and once in a debug admin path —
	// and `go test -race` catches the unsynchronized writes from
	// concurrent runOnce calls immediately. The mutex is cheap (we
	// hold it only around the bookkeeping reads/writes, not around
	// SQL or DSL resolver calls) and turns "two callers corrupt the
	// cursor" into "two callers serialize."
	mu sync.Mutex

	// watermark + watermarkID together form the high-water mark of
	// successfully-handled rows; the next tick scans rows whose
	// (completed_at, id) tuple is strictly greater than this pair.
	// The id half is necessary because two pipeline_runs can complete
	// at the same nanosecond (parallel fan-out steps); a timestamp-only
	// watermark would re-pick the just-handled row on every tick
	// indefinitely.
	//
	// State is in-memory only. On process restart we reset to
	// (now - 1h, "") which conservatively re-scans the last hour;
	// the partial UNIQUE INDEX on eval_runs(pipeline_run_id) WHERE
	// kind='online' makes any re-handled row collapse to a no-op.
	watermark   time.Time
	watermarkID string

	// backoffSkips counts how many upcoming ticks to skip due to a
	// persistent entropy outage. Set by runOnce on cryptoSample()
	// failure, decremented by Start on each tick. Caps at
	// maxBackoffSkips so the sampler always comes back eventually.
	backoffSkips int
}

// maxBackoffSkips caps the entropy-outage exponential backoff so a
// long outage still results in periodic retry attempts (and journal
// alerts) instead of going silent forever.
const maxBackoffSkips = 10

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
//
// Start MUST be called exactly once per OnlineSampler instance.
// Two concurrent Start goroutines on the same instance will race
// on s.watermark / s.watermarkID; the internal mutex serializes
// them to avoid corruption, but the resulting behaviour is two
// tickers fighting for the same cursor and is operationally
// pointless. The bootstrap call sites wire this once at server
// init via cmd/crewship/main.go.
func (s *OnlineSampler) Start(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	// Run one tick immediately so a startup picks up backlog without
	// waiting the full interval. Subsequent ticks come from the ticker.
	s.tickWithBackoff(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tickWithBackoff(ctx)
		}
	}
}

// tickWithBackoff consults the backoff counter (set by runOnce on
// persistent entropy outage) before deciding whether to actually call
// runOnce this round. Decrementing in Start rather than runOnce keeps
// the locked critical section narrow.
func (s *OnlineSampler) tickWithBackoff(ctx context.Context) {
	s.mu.Lock()
	skip := s.backoffSkips
	if skip > 0 {
		s.backoffSkips--
	}
	s.mu.Unlock()
	if skip > 0 {
		s.logger.Debug("online sampler tick skipped due to entropy backoff", "skips_remaining", skip-1)
		return
	}
	s.runOnce(ctx)
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
	completedAt string // RFC3339Nano string from pipeline_runs
}

// scanPageSize bounds one SQL fetch so a huge backlog can't pin a
// single connection in QueryContext for the full poll. We page on
// completed_at (strictly monotonic per row because pipeline_runs uses
// timestamp-with-precision keys) and advance the watermark by the
// LAST row we ACTUALLY processed, never by an arbitrary wall-clock
// scanEnd. That closes the silent-data-loss path where LIMIT 500 hit
// a saturated window and rows beyond the cap got dropped because the
// watermark jumped to scanEnd regardless.
const scanPageSize = 500

// runOnce executes a single scan + enqueue pass. Pages through the
// pipeline_runs window in scanPageSize batches; advances the watermark
// only past rows that were actually iterated. Transient per-row
// failures (DSL resolver outage, INSERT error other than UNIQUE
// conflict) leave the watermark short of those rows so the next tick
// retries them.
//
// The whole body runs under s.mu so the watermark + watermarkID
// reads/writes are race-free even if Start is wired twice. We hold
// the lock across SQL too — that's intentional: paging through a
// large backlog mid-tick is rare and we'd rather serialize than risk
// two callers leapfrogging each other's watermark advances. The lock
// is per-instance; multiple OnlineSamplers (rare but possible in
// multi-process tests) don't contend.
func (s *OnlineSampler) runOnce(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	scanEnd := time.Now().UTC()
	scanEndStr := scanEnd.Format(time.RFC3339Nano)

	totalScanned := 0
	totalEnqueued := 0
	entropyOutages := 0
	hadRetryableErr := false

	// Track the pagination cursor as a (completed_at, id) tuple.
	// Using completed_at alone is insufficient because two
	// pipeline_runs can complete at the same nanosecond — common when
	// a routine fans out parallel steps that finish in the same
	// scheduler tick. With `> cursor_ts` on the next page query,
	// the sibling row with the same timestamp gets skipped forever
	// once one of them has been handled. Adding `id` as a deterministic
	// tie-breaker (every pipeline_run has a unique id) closes the
	// hole; the ORDER BY mirrors the cursor predicate so a row's
	// position is fully determined.
	//
	// lastHandledTS / lastHandledID track the high-water mark for
	// successfully-handled rows (advance-on-success). lastInPage
	// tracks where the SQL cursor moves to for the NEXT page; on a
	// retryable error we hold the watermark but still move the
	// cursor so we don't loop on the same window — the next tick
	// will rescan everything we paused on.
	startTS := s.watermark.Format(time.RFC3339Nano)
	startID := s.watermarkID
	lastHandledTS := startTS
	lastHandledID := startID
	cursorTS := startTS
	cursorID := startID

	for {
		rows, err := s.db.QueryContext(ctx, `
SELECT id, workspace_id, pipeline_id, pipeline_slug, completed_at
FROM pipeline_runs
WHERE status = 'completed'
  AND completed_at IS NOT NULL
  AND completed_at < ?
  AND (completed_at > ? OR (completed_at = ? AND id > ?))
ORDER BY completed_at ASC, id ASC
LIMIT ?
`, scanEndStr, cursorTS, cursorTS, cursorID, scanPageSize)
		if err != nil {
			s.logger.Warn("online sampler scan failed", "err", err)
			return
		}

		candidates := make([]scannedRun, 0, scanPageSize)
		for rows.Next() {
			var r scannedRun
			if err := rows.Scan(&r.id, &r.workspaceID, &r.pipelineID, &r.slug, &r.completedAt); err != nil {
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
		if len(candidates) == 0 {
			break
		}

		// Once a retryable error fires inside a page, freeze the
		// (lastHandled) watermark — every row AFTER the failure must
		// wait for the next tick to keep ordering. Without this, a
		// failed row followed by a successful row would advance the
		// watermark past the failure and orphan it forever. We still
		// iterate through the rest of the page so the loop counters
		// (totalScanned, entropyOutages) reflect what was seen.
		stuck := false
		advance := func(ts, id string) {
			if !stuck {
				lastHandledTS = ts
				lastHandledID = id
			}
		}

		for _, r := range candidates {
			totalScanned++

			dsl, err := s.dslResolver.GetDSLByPipelineID(ctx, r.pipelineID)
			if err != nil {
				stuck = true
				hadRetryableErr = true
				continue
			}
			if dsl == nil || dsl.Eval == nil || dsl.Eval.Online == nil {
				advance(r.completedAt, r.id)
				continue
			}
			rate := dsl.Eval.Online.SampleRate
			if rate <= 0 {
				advance(r.completedAt, r.id)
				continue
			}
			if dsl.Eval.Online.GraderAgentSlug == "" {
				// No grader configured = nothing to enqueue against.
				// Deterministic skip — advance so we don't re-scan a
				// misconfigured routine every tick forever.
				advance(r.completedAt, r.id)
				continue
			}
			if rate < 1.0 {
				sample, ok := cryptoSample()
				if !ok {
					entropyOutages++
					stuck = true
					hadRetryableErr = true
					continue
				}
				if sample >= rate {
					advance(r.completedAt, r.id)
					continue
				}
			}

			if err := s.enqueue(ctx, r.workspaceID, r.slug, r.id, dsl.Eval.Online); err != nil {
				s.logger.Warn("online sampler enqueue failed",
					"err", err, "run_id", r.id, "pipeline_id", r.pipelineID)
				stuck = true
				hadRetryableErr = true
				continue
			}
			totalEnqueued++
			advance(r.completedAt, r.id)
		}

		// If we got stuck mid-page, end the tick — next tick retries
		// from the held watermark. Otherwise move the SQL cursor to
		// the last (completed_at, id) tuple in this page so
		// pagination makes progress through a long deterministic-skip
		// backlog without overlapping with the previous page.
		if stuck {
			break
		}
		last := candidates[len(candidates)-1]
		cursorTS = last.completedAt
		cursorID = last.id
		if len(candidates) < scanPageSize {
			break
		}
	}

	_ = scanEnd // referenced only for the scanEndStr bound above; keeps intent obvious.

	// Watermark advances to the (completed_at, id) high-water mark of
	// successfully-handled rows. Retryable per-row errors hold both
	// halves back so the next tick gets another shot at the same rows.
	parsed, err := time.Parse(time.RFC3339Nano, lastHandledTS)
	if err == nil {
		s.watermark = parsed.UTC()
		s.watermarkID = lastHandledID
	} else {
		// Defensive: never advance to garbage. The startup watermark is
		// scanEnd - 1h; if parsing fails we leave the existing value.
		s.logger.Warn("watermark parse failed; leaving in place", "value", lastHandledTS, "err", err)
	}

	if entropyOutages > 0 {
		// Doubling-skip backoff per consecutive outage: each tick that
		// hits the entropy floor schedules 2× as many tick-skips as
		// the last (1 → 2 → 4 → 8 → …), capped at maxBackoffSkips so
		// the sampler always comes back periodically and the operator
		// keeps seeing the warn log. Without backoff, a persistent
		// /dev/urandom outage at a fixed 60 s tick rate would spam
		// the journal at 60 Hz forever AND keep refusing to advance,
		// growing the backlog unbounded.
		current := s.backoffSkips
		next := current * 2
		if next < 1 {
			next = 1
		}
		if next > maxBackoffSkips {
			next = maxBackoffSkips
		}
		s.backoffSkips = next
		s.logger.Warn("online sampler hit entropy outage; deferred rows for next tick",
			"deferred", entropyOutages,
			"backoff_skips_set", next)
	} else if s.backoffSkips > 0 {
		// Successful tick clears the backoff entirely so steady-state
		// healthy operation doesn't carry residual delay.
		s.backoffSkips = 0
	}
	if totalScanned > 0 {
		s.logger.Debug("online sampler tick",
			"scanned", totalScanned,
			"enqueued", totalEnqueued,
			"retryable_errors", hadRetryableErr,
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
	// INSERT OR IGNORE on the partial UNIQUE INDEX uq_eval_runs_online_pipeline_run
	// makes the enqueue idempotent at the schema layer. A duplicate sampler
	// instance, a watermark-replay after crash recovery, or a retry that
	// crossed paths with a successful prior insert all collapse to a no-op
	// rather than enqueueing the same pipeline_run twice and double-billing
	// the grader.
	result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO eval_runs
    (id, workspace_id, kind, status, routine_slug, pipeline_run_id, sample_rate)
VALUES (?, ?, 'online', 'queued', ?, ?, ?)
`, id, workspaceID, routineSlug, pipelineRunID, cfg.SampleRate)
	if err != nil {
		return err
	}
	// RowsAffected==0 means the UNIQUE conflict triggered — the duplicate
	// is benign, just don't emit a misleading journal entry saying we
	// just queued one. We also skip the journal entry in that path so
	// dashboards don't double-count.
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil
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

// cryptoSample returns a float in [0,1) drawn from crypto/rand, plus
// an ok flag. Used instead of math/rand so the sampling pattern can't
// be predicted from a known PRNG seed.
//
// On entropy outage (/dev/urandom unreadable — rare in production but
// possible in container-init / restricted-sysfs setups) the function
// returns (0, false). The original cut returned a fixed 0.5 and
// claimed in a comment that this "over-samples on entropy outage,
// the safer side for observability." That was wrong in both directions:
// the decision rule in runOnce is `if cryptoSample() >= rate
// { continue }`, so 0.5 at the realistic production rate of 0.05
// flips the condition to `0.5 >= 0.05 ⇒ true ⇒ skip every row`,
// producing 0% sample coverage exactly when the operator most needs
// the trail. Returning (_, false) and skipping the tick instead is
// the only honest fail-safe — we lose the sample but don't lie about
// having taken it.
func cryptoSample() (float64, bool) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, false
	}
	// Mask to 53 bits (float64 mantissa) and divide for uniform [0,1).
	u := binary.BigEndian.Uint64(b[:]) & ((1 << 53) - 1)
	return float64(u) / (1 << 53), true
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
