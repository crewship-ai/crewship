package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// defaultDispatchConcurrency bounds how many claimed rows dispatch at
// once. Deferred runs each hold an executor slot for the whole routine,
// so this caps how many concurrent provider-bound runs one sweep can
// launch. Sized in the 8–16 band from #834: high enough that co-due
// runs start together, low enough not to stampede the provider.
const defaultDispatchConcurrency = 12

// runExecutor is the slice of *Executor the dispatcher needs. Narrowing
// to an interface keeps executor.go untouched while letting tests inject
// a fake slow runner to prove concurrency.
type runExecutor interface {
	Run(ctx context.Context, in RunInput) (*RunResult, error)
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

	sem     chan struct{}  // bounded worker pool; sized at first sweep
	wg      sync.WaitGroup // tracks in-flight dispatch goroutines
	stopCh  chan struct{}
	stopped chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
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
		stopCh:         make(chan struct{}),
		stopped:        make(chan struct{}),
	}
}

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

	t := time.NewTicker(d.tick)
	defer t.Stop()
	d.sweep(ctx)
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
	now := time.Now().UTC()
	if n, err := d.store.ExpireDue(ctx, now); err != nil {
		d.logger.Warn("pending dispatcher: expire", "error", err)
	} else if n > 0 {
		d.logger.Info("pending dispatcher: expired past-ttl runs", "count", n)
	}
	due, err := d.store.DueRuns(ctx, now, 25)
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
	// Claim the row first so a second tick (or replica) can't double-fire.
	claimed, err := d.store.MarkFired(ctx, pr.ID, "")
	if err != nil {
		d.logger.Warn("pending dispatcher: claim", "error", err, "pending_id", pr.ID)
		return
	}
	if !claimed {
		return // already fired/cancelled/expired by someone else
	}

	var inputs map[string]any
	if pr.InputsJSON != "" {
		_ = json.Unmarshal([]byte(pr.InputsJSON), &inputs)
	}
	var tags []string
	if pr.TagsJSON != "" {
		_ = json.Unmarshal([]byte(pr.TagsJSON), &tags)
	}

	res, runErr := d.executor.Run(ctx, RunInput{
		PipelineID:    pr.PipelineID,
		WorkspaceID:   pr.WorkspaceID,
		Inputs:        inputs,
		Mode:          ModeRun,
		TierOverride:  Complexity(pr.TierOverride),
		TriggeredVia:  TriggeredViaSchedule,
		TriggeredByID: pr.ID,
		Tags:          tags,
		MetadataJSON:  pr.MetadataJSON,
		// Each pending row is a one-shot fired once (MarkFired claims it above);
		// key on the row ID as a second guard so a re-dispatch of the same row
		// (debounce coalescing, restart) dedupes at the executor rather than
		// producing a second run.
		IdempotencyKey: ScheduledFireIdempotencyKey("pending", pr.ID, "once"),
	})
	if runErr != nil {
		d.logger.Warn("pending dispatcher: run failed", "error", runErr, "pending_id", pr.ID)
		return
	}
	// Backfill the fired run id now that we have it (claim used "").
	if res != nil {
		if uerr := d.store.SetFiredRunID(ctx, pr.ID, runIDOf(res)); uerr != nil {
			d.logger.Warn("pending dispatcher: backfill run id", "error", uerr, "pending_id", pr.ID)
		}
	}
}

// runIDOf extracts the run id from a RunResult via JSON to avoid
// coupling to its concrete field layout.
func runIDOf(res *RunResult) string {
	b, err := json.Marshal(res)
	if err != nil {
		return ""
	}
	var m struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(b, &m)
	return m.RunID
}
