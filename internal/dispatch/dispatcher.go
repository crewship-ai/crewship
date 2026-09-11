package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

// Dispatcher is the single owner of execution.
type Dispatcher struct {
	store   *work.Store
	runtime Runtime
	authz   Authorizer
	cfg     Config
	logger  *slog.Logger

	// hints carries wake-up nudges. It is buffered and dropped when full on
	// purpose: a hint is an optimisation, and blocking an acceptance handler to
	// deliver one would make the optional thing mandatory.
	hints chan string

	mu      sync.Mutex
	running map[string]*liveAttempt
}

type liveAttempt struct {
	assignment Assignment
	locator    string
	cancel     context.CancelFunc
}

// New builds a dispatcher. It does not start anything; call Run.
func New(store *work.Store, runtime Runtime, authz Authorizer, cfg Config, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		store:   store,
		runtime: runtime,
		authz:   authz,
		cfg:     cfg.withDefaults(),
		logger:  logger,
		hints:   make(chan string, 128),
		running: map[string]*liveAttempt{},
	}
}

// Hint tells the dispatcher there may be work now.
//
// It never blocks and never reports failure, because a caller must not be able
// to make acceptance depend on it. Losing every hint costs latency, not
// correctness: the poll finds the same work.
func (d *Dispatcher) Hint(workID string) {
	select {
	case d.hints <- workID:
	default:
	}
}

// Run drives the loop until ctx ends.
//
// On the way in it recovers: attempts whose lease expired are returned to the
// queue if nothing external was ever attempted, and parked for reconciliation
// if a runtime may exist. That ordering matters — recovering before claiming
// means a restart cannot start a second runtime for work the previous process
// had already begun.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.recover(ctx); err != nil {
		d.logger.Error("dispatch: recovery pass failed; not claiming until it succeeds", "error", err)
		return err
	}

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		// Drain as much as capacity allows before waiting again.
		for {
			claimed, err := d.claimOne(ctx)
			if err != nil {
				d.logger.Error("dispatch: claim failed", "error", err)
				break
			}
			if !claimed {
				break
			}
		}

		select {
		case <-ctx.Done():
			d.drain()
			return nil
		case <-ticker.C:
		case <-d.hints:
		}
	}
}

// recover runs one lease-recovery pass and then reconciles what it parked.
func (d *Dispatcher) recover(ctx context.Context) error {
	out, err := d.store.RecoverExpiredLeases(ctx)
	if err != nil {
		return fmt.Errorf("recover expired leases: %w", err)
	}
	if len(out.Requeued) > 0 || len(out.Reconciliation) > 0 {
		d.logger.Info("dispatch: recovered abandoned attempts",
			"requeued", len(out.Requeued), "reconciliation", len(out.Reconciliation))
	}
	return nil
}

// claimOne takes at most one piece of work and starts it. It reports whether it
// claimed anything, so the caller can drain.
func (d *Dispatcher) claimOne(ctx context.Context) (bool, error) {
	c, err := d.store.Claim(ctx, work.ClaimOptions{
		LeaseOwner: d.cfg.Owner,
		Limits:     d.cfg.Limits,
	})
	if errors.Is(err, work.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	a := Assignment{Item: c.Item, RunID: c.RunID, Attempt: c.Attempt, Generation: c.Generation}

	// A cancel may have been recorded while this work sat queued. Checking
	// after the claim and BEFORE anything external is the only place a cancel
	// can be honoured for free: nothing has started, so stopping is confirmed
	// rather than requested.
	if stop, err := d.cancelledBeforeStart(ctx, a); err != nil {
		return true, err
	} else if stop {
		return true, nil
	}

	// I8: re-check here, not at acceptance. Work waits for capacity, and in
	// that gap a permission can be revoked, a budget spent or a target deleted.
	if d.authz != nil {
		refusal, err := d.authz.Authorize(ctx, a)
		if err != nil {
			// Could not decide. Not permission, and not a failure of the work
			// either — park it rather than guess in either direction.
			d.park(ctx, a, "authorization could not be checked at dispatch: "+err.Error())
			return true, nil
		}
		if refusal != "" {
			d.finish(ctx, a, work.StateFailed, "refused at dispatch: "+refusal)
			return true, nil
		}
	}

	d.start(ctx, a)
	return true, nil
}

// cancelledBeforeStart honours a cancel recorded against work that has not
// started. Reports whether it took the work.
func (d *Dispatcher) cancelledBeforeStart(ctx context.Context, a Assignment) (bool, error) {
	requested, err := d.store.CancelRequested(ctx, a.RunID)
	if err != nil {
		return false, fmt.Errorf("read cancel request: %w", err)
	}
	if !requested {
		return false, nil
	}
	// Nothing was created, so this is a confirmed stop and may honestly be
	// called cancelled.
	if err := d.store.Transition(ctx, work.TransitionRequest{
		WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation,
		To: work.StateCancelled, Reason: "cancelled after claim, before any runtime was created",
	}); err != nil {
		return true, fmt.Errorf("cancel before start: %w", err)
	}
	return true, nil
}

// start records the start intent, creates the runtime, and owns the attempt
// until it ends.
func (d *Dispatcher) start(ctx context.Context, a Assignment) {
	locator := d.runtime.Locator(a)

	// Written BEFORE the runtime exists. A crash between here and the process
	// leaves recovery an identity to look for; a crash before here leaves an
	// attempt that provably started nothing.
	if err := d.store.MarkStarting(ctx, a.Item.ID, a.RunID, a.Generation, locator); err != nil {
		d.logger.Error("dispatch: could not record the start intent; not starting a runtime",
			"work_id", a.Item.ID, "run_id", a.RunID, "error", err)
		d.park(ctx, a, "start intent could not be recorded: "+err.Error())
		return
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	live := &liveAttempt{assignment: a, locator: locator, cancel: cancel}
	d.mu.Lock()
	d.running[a.RunID] = live
	d.mu.Unlock()

	go d.supervise(runCtx, live)
}

// supervise runs the attempt: heartbeat, cancel watch, and the runtime itself.
func (d *Dispatcher) supervise(ctx context.Context, live *liveAttempt) {
	a := live.assignment
	defer func() {
		d.mu.Lock()
		delete(d.running, a.RunID)
		d.mu.Unlock()
		live.cancel()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.heartbeat(ctx, a) }()
	go func() { defer wg.Done(); d.watchCancel(ctx, live) }()

	confirmOnce := sync.Once{}
	runErr := d.runtime.Run(ctx, a, func() {
		confirmOnce.Do(func() {
			if err := d.store.StartRunning(ctx, a.Item.ID, a.RunID, a.Generation, live.locator); err != nil {
				// The runtime exists and we could not record it. Do NOT treat
				// that as "it never started" — that inference is the whole
				// reason the start intent is written first.
				d.logger.Error("dispatch: runtime started but could not be confirmed",
					"work_id", a.Item.ID, "run_id", a.RunID, "locator", live.locator, "error", err)
			}
		})
	})

	live.cancel()
	wg.Wait()

	d.settle(context.WithoutCancel(ctx), live, runErr)
}

// settle writes the attempt's outcome.
func (d *Dispatcher) settle(ctx context.Context, live *liveAttempt, runErr error) {
	a := live.assignment

	// A cancel that was asked for is only a cancellation once the runtime is
	// confirmed gone. Anything less is reconciliation — §4 is explicit that an
	// unstoppable or unclear process is not a cancelled one.
	requested, err := d.store.CancelRequested(ctx, a.RunID)
	if err != nil {
		d.logger.Error("dispatch: could not read the cancel request while settling", "run_id", a.RunID, "error", err)
	}
	if requested {
		alive, err := d.runtime.Alive(ctx, live.locator)
		switch {
		case err != nil:
			d.park(ctx, a, "cancel requested and the runtime could not be checked: "+err.Error())
		case alive:
			d.park(ctx, a, "cancel requested and the runtime is still alive at "+live.locator)
		default:
			d.finish(ctx, a, work.StateCancelled, "runtime confirmed stopped after a cancel request")
		}
		return
	}

	if runErr == nil {
		d.finish(ctx, a, work.StateSucceeded, "completed")
		return
	}

	// A failed run may be retried — but only when the failure is the run's own.
	// An unclear EXTERNAL effect is never retried automatically: repeating an
	// agent turn whose side effects may already have landed is exactly what
	// reconciliation exists to prevent.
	if errors.Is(runErr, ErrUnclearOutcome) {
		d.park(ctx, a, "the runtime's outcome is unclear: "+runErr.Error())
		return
	}
	d.finish(ctx, a, work.StateRetryWait, runErr.Error())
}

// ErrUnclearOutcome marks a runtime failure whose external effects may or may
// not have happened. It routes to reconciliation rather than to a retry.
var ErrUnclearOutcome = errors.New("dispatch: the runtime's outcome is unclear")

func (d *Dispatcher) finish(ctx context.Context, a Assignment, to work.State, reason string) {
	if err := d.store.Transition(ctx, work.TransitionRequest{
		WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: to, Reason: reason,
	}); err != nil {
		d.logger.Error("dispatch: could not record the outcome",
			"work_id", a.Item.ID, "run_id", a.RunID, "to", to, "error", err)
	}
}

func (d *Dispatcher) park(ctx context.Context, a Assignment, reason string) {
	d.finish(ctx, a, work.StateNeedsReconciliation, reason)
}

// heartbeat renews the lease while the attempt runs. A refusal means this
// attempt has been superseded, and the right response is to stop renewing a
// lease we no longer hold rather than to keep asserting it.
func (d *Dispatcher) heartbeat(ctx context.Context, a Assignment) {
	t := time.NewTicker(d.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.store.Heartbeat(ctx, a.RunID, a.Generation); err != nil {
				if errors.Is(err, work.ErrStaleGeneration) {
					d.logger.Warn("dispatch: this attempt has been superseded; stopping its heartbeat",
						"run_id", a.RunID)
					return
				}
				d.logger.Warn("dispatch: heartbeat failed", "run_id", a.RunID, "error", err)
			}
		}
	}
}

// watchCancel notices a stop requested elsewhere — by another process, or
// before this one restarted — and signals the runtime.
func (d *Dispatcher) watchCancel(ctx context.Context, live *liveAttempt) {
	t := time.NewTicker(d.cfg.CancelPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			requested, err := d.store.CancelRequested(ctx, live.assignment.RunID)
			if err != nil || !requested {
				continue
			}
			if _, err := d.runtime.Stop(ctx, live.locator); err != nil {
				d.logger.Warn("dispatch: could not signal the runtime to stop",
					"run_id", live.assignment.RunID, "locator", live.locator, "error", err)
			}
			// Settling decides what actually happened; this goroutine's job is
			// only to deliver the signal once.
			return
		}
	}
}

// drain stops supervising without pretending the runtimes are gone.
//
// Shutting down must not leave processes running while the ledger calls them
// finished. Anything still live is parked for reconciliation, which is a
// truthful "somebody has to look" rather than a convenient "it stopped".
func (d *Dispatcher) drain() {
	d.mu.Lock()
	live := make([]*liveAttempt, 0, len(d.running))
	for _, l := range d.running {
		live = append(live, l)
	}
	d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.StopGrace)
	defer cancel()
	for _, l := range live {
		stopped, err := d.runtime.Stop(ctx, l.locator)
		if err == nil && stopped {
			d.finish(ctx, l.assignment, work.StateCancelled, "stopped during shutdown")
			continue
		}
		d.park(ctx, l.assignment,
			"the server shut down while this runtime was live at "+l.locator+"; it was not confirmed stopped")
	}
}
