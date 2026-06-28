package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// PendingRunDispatcher fires deferred runs (pending_runs, v122). Every
// tick it expires past-ttl rows, then dispatches due rows — highest
// priority first — through the executor. Mirrors PipelineScheduler's
// lifecycle so cmd_start.go wires it the same way.
type PendingRunDispatcher struct {
	store    *PendingRunStore
	executor *Executor
	logger   *slog.Logger
	tick     time.Duration

	stopCh    chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewPendingRunDispatcher builds the dispatcher. A 5s tick keeps short
// delays responsive without hammering the DB.
func NewPendingRunDispatcher(store *PendingRunStore, executor *Executor, logger *slog.Logger) *PendingRunDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &PendingRunDispatcher{
		store:    store,
		executor: executor,
		logger:   logger,
		tick:     5 * time.Second,
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
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
// until the stop signal or context cancellation.
func (d *PendingRunDispatcher) run(ctx context.Context) {
	defer close(d.stopped)
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

// sweep expires past-ttl rows, then fires the due rows (priority-first).
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
		d.fireOne(ctx, pr)
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
