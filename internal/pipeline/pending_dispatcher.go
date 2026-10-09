package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/jitter"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// defaultDispatchConcurrency bounds how many claimed rows dispatch at
// once. Deferred runs each hold an executor slot for the whole routine,
// so this caps how many concurrent provider-bound runs one sweep can
// launch. Sized in the 8–16 band from #834: high enough that co-due
// runs start together, low enough not to stampede the provider.
const defaultDispatchConcurrency = 12

// prewarmTimeout bounds a single prewarm attempt so a wedged provider can't
// leak the off-critical-path goroutine long past the run it was warming for.
const prewarmTimeout = 2 * time.Minute

// runExecutor is the slice of *Executor the dispatcher needs. Narrowing
// to an interface keeps executor.go untouched while letting tests inject
// a fake slow runner to prove concurrency.
type runExecutor interface {
	Run(ctx context.Context, in RunInput) (*RunResult, error)
}

// runPrewarmer is the optional capability the dispatcher uses to warm a run's
// crew container at claim time, ahead of the blocking Run (#836). The
// production *Executor implements it; a bare fake in a test may not, so it's
// probed via a type assertion.
type runPrewarmer interface {
	PrewarmForRun(ctx context.Context, pipelineID, workspaceID string)
}

// PendingRunDispatcher fires deferred runs (pending_runs, v122). Every
// tick it expires past-ttl rows, then dispatches due rows — highest
// priority first — through the executor. Each claimed row is dispatched
// on its own goroutine, bounded by a worker pool, so a single slow run
// no longer blocks every other co-due run (the old serial+synchronous
// sweep had throughput 1/run-duration). Mirrors PipelineScheduler's
// lifecycle so cmd_start.go wires it the same way.
//
// Ordering: priority is best-effort at claim time — rows are claimed in
// priority order but then run concurrently, so completion order is not
// guaranteed. MarkFired is an atomic winner-takes-once claim, so a
// following sweep (or a replica) can never double-fire an in-flight row.
type PendingRunDispatcher struct {
	store          *PendingRunStore
	executor       runExecutor
	logger         *slog.Logger
	tick           time.Duration
	maxConcurrency int
	now            func() time.Time // injectable clock for backoff/TTL boundary tests

	sem     chan struct{}  // bounded worker pool; sized at first sweep
	wg      sync.WaitGroup // tracks in-flight dispatch goroutines
	stopCh  chan struct{}
	stopped chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	// paused, when set, holds every sweep (internal/quiesce.QueuePaused).
	paused func() bool
}

// NewPendingRunDispatcher builds the dispatcher. A 5s tick keeps short
// delays responsive without hammering the DB.
func NewPendingRunDispatcher(store *PendingRunStore, executor runExecutor, logger *slog.Logger) *PendingRunDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &PendingRunDispatcher{
		store:          store,
		executor:       executor,
		logger:         logger,
		tick:           5 * time.Second,
		maxConcurrency: defaultDispatchConcurrency,
		now:            time.Now,
		stopCh:         make(chan struct{}),
		stopped:        make(chan struct{}),
	}
}

// SetPaused makes every sweep a no-op while fn reports true (see sweep).
// Call before Start.
func (d *PendingRunDispatcher) SetPaused(fn func() bool) { d.paused = fn }

// Start spawns the dispatch loop. Idempotent.
func (d *PendingRunDispatcher) Start(ctx context.Context) {
	d.startOnce.Do(func() { go d.run(ctx) })
}

// Stop signals the loop to exit and blocks until it does. Idempotent.
func (d *PendingRunDispatcher) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
		<-d.stopped
	})
}

// run is the dispatch loop: sweep once on start, then on every tick
// until the stop signal or context cancellation. Defers run last-in-
// first-out, so in-flight dispatch goroutines drain (wg.Wait) before
// the loop signals it has stopped — Stop() therefore returns only once
// every fired run has been handed off.
func (d *PendingRunDispatcher) run(ctx context.Context) {
	defer close(d.stopped)
	defer d.wg.Wait()

	if d.maxConcurrency < 1 {
		d.maxConcurrency = defaultDispatchConcurrency
	}
	d.sem = make(chan struct{}, d.maxConcurrency)

	d.sweep(ctx)
	// The boot sweep stays immediate; only the ticker's phase is spread from
	// the other sweepers started at boot (#1891).
	if !jitter.Wait(ctx, d.stopCh, d.tick) {
		return
	}
	t := time.NewTicker(d.tick)
	defer t.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			d.sweep(ctx)
		}
	}
}

// sweep expires past-ttl rows, then dispatches the due rows. Rows are
// walked in priority order; each is handed to a bounded worker pool so
// co-due runs start together instead of queueing behind the slowest.
// The pool acquire is interruptible so a Stop() mid-sweep abandons the
// not-yet-dispatched tail promptly rather than blocking on a full pool.
func (d *PendingRunDispatcher) sweep(ctx context.Context) {
	// Held (an instance restore's queue hold, a backup's quiet window):
	// neither fire nor expire. A row that would have expired meanwhile is
	// decided on the first sweep after the hold, not lost during it.
	if d.paused != nil && d.paused() {
		return
	}
	// The expire pass and the listing are one writer in the backup's quiet
	// window barrier; each claim below is its own (fireOne).
	wr, ok := quiesce.Enter(ctx)
	if !ok {
		return
	}
	defer wr.Leave()
	now := d.now().UTC()
	if n, err := d.store.ExpireDue(ctx, now); err != nil {
		d.logger.Warn("pending dispatcher: expire", "error", err)
	} else if n > 0 {
		d.logger.Info("pending dispatcher: expired past-ttl runs", "count", n)
	}
	due, err := d.store.DueRuns(ctx, now, 25)
	// Never hold the sweep writer while waiting for worker slots: a worker
	// may itself be waiting for a closing quiet window to release.
	wr.Leave()
	if err != nil {
		d.logger.Warn("pending dispatcher: list due", "error", err)
		return
	}
	for _, pr := range due {
		// Acquire a pool slot before spawning so total in-flight stays
		// bounded (no unbounded goroutine growth across sweeps). Bail if
		// we're stopping or the context is cancelled.
		select {
		case d.sem <- struct{}{}:
		case <-d.stopCh:
			return
		case <-ctx.Done():
			return
		}
		d.wg.Add(1)
		go func(pr PendingRun) {
			defer d.wg.Done()
			defer func() { <-d.sem }()
			d.fireOne(ctx, pr)
		}(pr)
	}
}

// fireOne claims a due pending row (winner-takes-once) and dispatches it
// through the executor, then backfills the resulting run id.
func (d *PendingRunDispatcher) fireOne(ctx context.Context, pr PendingRun) {
	// The run is admitted by the backup's quiet window BEFORE the claim, so
	// it counts as busy from the claim on — not only once its row reaches
	// running. A window that is closing or held refuses: the row is not
	// claimed and stays due for the first sweep after release.
	adm, ok := quiesce.StartRun(ctx)
	if !ok {
		return
	}
	defer adm.Done()
	// Claim the row first so a second tick (or replica) can't double-fire.
	// The claim is a writer in the barrier too; a window that closed since
	// the admission refuses it, and the row stays due.
	wr, ok := quiesce.Enter(ctx)
	if !ok {
		return
	}
	claimed, err := d.store.ClaimDue(ctx, pr.ID, d.now().UTC())
	wr.Leave()
	if err != nil {
		d.logger.Warn("pending dispatcher: claim", "error", err, "pending_id", pr.ID)
		return
	}
	if claimed == nil {
		return // already claimed, cancelled, expired or postponed
	}
	pr = *claimed

	// Prewarm the crew's container off the critical path: kick provisioning at
	// claim so the run's first agent step finds it warm instead of paying cold
	// container start inline (#836). Best-effort, idempotent (the provider's
	// per-crew lock collapses concurrent claims for one crew to a single start),
	// and side-effect-free (no run/cost event) — a miss only forfeits the
	// latency it was trying to save. Runs concurrently with the Run dispatch
	// below, overlapping the container start with routine/agent resolution.
	if pw, ok := d.executor.(runPrewarmer); ok {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			pctx, cancel := context.WithTimeout(ctx, prewarmTimeout)
			defer cancel()
			pw.PrewarmForRun(pctx, pr.PipelineID, pr.WorkspaceID)
		}()
	}

	var inputs map[string]any
	if pr.InputsJSON != "" {
		_ = json.Unmarshal([]byte(pr.InputsJSON), &inputs)
	}
	var tags []string
	if pr.TagsJSON != "" {
		_ = json.Unmarshal([]byte(pr.TagsJSON), &tags)
	}

	triggeredVia, triggeredByID := effectivePendingTrigger(pr)
	res, runErr := d.executor.Run(adm.Context(), RunInput{
		PinnedVersion: pr.PinnedVersion,
		PipelineID:    pr.PipelineID,
		WorkspaceID:   pr.WorkspaceID,
		Inputs:        inputs,
		Mode:          ModeRun,
		TierOverride:  Complexity(pr.TierOverride),
		// Honour what the row says started it. Before pending_runs carried
		// attribution this was hard-coded to schedule/self, so every
		// automation-fired run reported a cron.
		TriggeredVia:  triggeredVia,
		TriggeredByID: triggeredByID,
		// Spend from the same budget a call_pipeline hop spends from. A
		// deferred run is where a composed cycle re-enters the process, so
		// dropping the depth here is what made the cap unreachable.
		ChainDepth: pr.ChainDepth,
		// And which chain it belongs to. Empty stays empty so the executor roots
		// a scheduled run at itself; a value invented here would claim a parent
		// that does not exist.
		ChainOrigin: pr.ChainOrigin,
		// Thread the enqueuing user through so a notify step's `to: trigger`
		// in this deferred run resolves to them (issue #842 Phase 1); empty
		// keeps the workspace-notice fallback.
		InvokingUserID:      pr.InvokingUserID,
		InvocationAuthority: pr.InvocationAuthority,
		Tags:                tags,
		MetadataJSON:        pr.MetadataJSON,
		// A one-time authoring row can be rearmed for a different date. The
		// occurrence identifies the start; redispatch of that occurrence dedupes.
		IdempotencyKey: ScheduledFireIdempotencyKey("pending", pr.ID, pr.FireAt.UTC().Format(time.RFC3339Nano)),
	})
	// Run has returned; release busy admission before waiting on a writer gate.
	adm.Done()
	if runErr != nil && (res == nil || res.RunID == "") {
		d.logger.Warn("pending dispatcher: run failed", "error", runErr, "pending_id", pr.ID)
		d.recordDispatchError(ctx, pr, runErr)
		return
	}
	if runErr != nil {
		// A run exists, so its own record carries the failure; link it
		// rather than requeue a start that already ran.
		d.logger.Warn("pending dispatcher: run failed after it started", "error", runErr, "pending_id", pr.ID, "run_id", res.RunID)
	}
	if res == nil || res.RunID == "" {
		d.recordDispatchError(ctx, pr, errors.New("executor returned no run receipt"))
		return
	}
	// Backfill the fired run id now that we have it (claim used "").
	// The run has finished, so the busy probe no longer counts it: the
	// backfill is its own writer, and waits out a window rather than landing
	// inside one.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quiesce.DefaultHoldCap+quiesce.DefaultDrainTimeout+time.Minute)
	defer cancel()
	if uerr := quiesce.Do(writeCtx, func(ctx context.Context) error {
		return d.store.SetFiredRunID(ctx, pr, res.RunID)
	}); uerr != nil {
		d.logger.Warn("pending dispatcher: backfill run id", "error", uerr, "pending_id", pr.ID)
	}
}

// No TTL was historically a valid admission. Bound that legacy case to ten
// capacity attempts; an explicit TTL instead remains the retry deadline.
const maxPendingAttemptsWithoutTTL = 10

func pendingCapacityBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= 6 {
		return time.Minute
	}
	return (2 * time.Second) << (attempt - 1)
}

func (d *PendingRunDispatcher) recordDispatchError(ctx context.Context, pr PendingRun, runErr error) {
	now := d.now().UTC()
	status, reason := "failed", "Deferred start could not be dispatched. Check server logs using the pending ID."
	var next *time.Time
	// A dispatch cut short by cancellation (server shutdown or deploy) never
	// produced a run, so it is retried like a capacity refusal instead of
	// being finalized as failed. fireOne only gets here without a run ID.
	interrupted := errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || ctx.Err() != nil
	if errors.Is(runErr, ErrConcurrencyLimitReached) || interrupted {
		waiting := "Waiting for routine execution capacity."
		if interrupted && !errors.Is(runErr, ErrConcurrencyLimitReached) {
			waiting = "Dispatch was interrupted before the run started; it will be retried."
		}
		switch {
		case pr.ExpiresAt != nil && !now.Before(*pr.ExpiresAt):
			status, reason = "expired", "Deferred start expired before execution capacity became available."
		case pr.ExpiresAt == nil && pr.DispatchAttempts >= maxPendingAttemptsWithoutTTL:
			reason = "Execution capacity remained unavailable after 10 dispatch attempts; no TTL was supplied."
		default:
			status, reason = "pending", waiting
			at := now.Add(pendingCapacityBackoff(pr.DispatchAttempts))
			if pr.ExpiresAt != nil && at.After(*pr.ExpiresAt) {
				at = *pr.ExpiresAt
			}
			next = &at
		}
	} else if errors.Is(runErr, ErrPinnedVersionNotFound) {
		reason = "The accepted recipe version is no longer available."
	} else if errors.Is(runErr, ErrRoutineNotActive) {
		reason = "The routine is no longer active."
	}
	// Finalize even when the execution context was canceled at shutdown. This
	// bounded write can wait out a full default backup window and never sleeps
	// through a capacity retry interval.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quiesce.DefaultHoldCap+quiesce.DefaultDrainTimeout+time.Minute)
	defer cancel()
	if err := quiesce.Do(writeCtx, func(ctx context.Context) error {
		return d.store.FinishDispatchError(ctx, pr, status, reason, next)
	}); err != nil {
		d.logger.Error("pending dispatcher: persist dispatch failure", "pending_id", pr.ID, "error", err)
	}
}
