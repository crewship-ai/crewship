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

	// clock is nil in production. Tests set it so a deferral's wait can be
	// asserted without spending it.
	clock func() time.Time
}

// now is the dispatcher's clock, overridable in tests that need a deferral to
// come back without waiting for it.
func (d *Dispatcher) now() time.Time {
	if d.clock != nil {
		return d.clock()
	}
	return time.Now()
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
	// A dispatcher that declared nothing claims everything, which is the same
	// harm as declaring the wrong thing: work it cannot execute is taken, its
	// generation moves, its attempt is burned, and the executor that could have
	// run it never sees it again. There is no safe default to fall back on —
	// only the caller knows what its Runtime can run — so this refuses to start
	// rather than starting something indiscriminate.
	if len(d.cfg.Kinds) == 0 {
		return errors.New("dispatch: no executable kinds declared; a dispatcher must say " +
			"what it can run, because claiming work it cannot run destroys it")
	}
	if err := d.recover(ctx); err != nil {
		d.logger.Error("dispatch: recovery pass failed; not claiming until it succeeds", "error", err)
		return err
	}

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()
	// Recovery on a timer, not only at boot. A server that restarts BEFORE an
	// old lease expires skips that lease on the way up, and with a boot-only
	// pass nothing ever looks again — the work hangs forever, and the symptom
	// is silence rather than an error.
	recovery := time.NewTicker(d.cfg.RecoveryInterval)
	defer recovery.Stop()

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
		case <-recovery.C:
			if err := d.recover(ctx); err != nil {
				d.logger.Error("dispatch: periodic recovery failed", "error", err)
			}
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
		Kinds:      d.cfg.Kinds,
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
		decision, err := d.authz.Authorize(ctx, a)
		if err != nil {
			// Could not decide. Not permission, and not a failure of the work
			// either — park it rather than guess in either direction.
			d.park(ctx, a, "authorization could not be checked at dispatch: "+err.Error())
			return true, nil
		}
		switch decision.kind {
		case decisionRefuse:
			d.finish(ctx, a, work.StateFailed, "refused at dispatch: "+decision.reason)
			return true, nil
		case decisionNotYet:
			// Back on the queue with the attempt given back. Nothing was
			// started — the start intent is not written until below — so the
			// budget is genuinely unspent, and the store re-checks that rather
			// than taking this code's word for it.
			until := d.now().Add(decision.retryAfter)
			if err := d.store.Defer(ctx, a.Item.ID, a.RunID, a.Generation, until,
				"deferred at dispatch: "+decision.reason); err != nil {
				d.logger.Error("dispatch: could not defer work the authorizer held",
					"work_id", a.Item.ID, "run_id", a.RunID, "error", err)
				d.park(ctx, a, "authorization deferred this work and it could not be requeued: "+err.Error())
			}
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
//
// It settles on whichever finishes first. That matters because a runtime is not
// guaranteed to return: a process that ignores its signal leaves Run blocked
// forever, and an earlier version of this waited for it — so a cancel nobody
// could enforce simply never settled, and the work stayed `running` with a
// request against it that nothing acted on.
func (d *Dispatcher) supervise(ctx context.Context, live *liveAttempt) {
	a := live.assignment
	defer func() {
		d.mu.Lock()
		delete(d.running, a.RunID)
		d.mu.Unlock()
		live.cancel()
	}()

	// superseded closes when this attempt loses its lease to a newer one.
	superseded := make(chan struct{})
	var supersededOnce sync.Once
	go d.heartbeat(ctx, a, func() { supersededOnce.Do(func() { close(superseded) }) })

	// abandoned closes when the cancel lifecycle gives up on a runtime that
	// will not stop. Settling then is the point: the work must not sit
	// `running` forever because the process refused to die.
	abandoned := make(chan struct{})
	var abandonOnce sync.Once
	go d.watchCancel(ctx, live, func() { abandonOnce.Do(func() { close(abandoned) }) })

	// Confirmation has two sources and needs only one.
	//
	// The stream event is a hint: it is fast, and it is what a chatty agent
	// gives us first. It is NOT the condition, because a silent process is
	// still a running one — an agent that starts, thinks for a minute and
	// prints nothing would otherwise never be recorded as running, and a
	// dispatcher that treats "no output yet" as "not started" is back to
	// inferring absence from silence.
	//
	// The condition is the provider's own answer: a runtime exists at the
	// locator. That is the contract Alive speaks, and it is true of a silent
	// process, a fast one, and one that has already exited having produced
	// nothing.
	var confirmOnce sync.Once
	confirm := func(how string) {
		confirmOnce.Do(func() {
			if err := d.store.StartRunning(ctx, a.Item.ID, a.RunID, a.Generation, live.locator); err != nil {
				// The runtime exists and we could not record it. Do NOT treat
				// that as "it never started" — that inference is the whole
				// reason the start intent is written first.
				d.logger.Error("dispatch: runtime started but could not be confirmed",
					"work_id", a.Item.ID, "run_id", a.RunID, "locator", live.locator,
					"confirmed_by", how, "error", err)
			}
		})
	}

	runDone := make(chan error, 1)
	go func() { runDone <- d.runtime.Run(ctx, a, func() { confirm("stream") }) }()
	go d.confirmByProbe(ctx, live, confirm)

	settleCtx := context.WithoutCancel(ctx)
	select {
	case runErr := <-runDone:
		live.cancel()
		d.settle(settleCtx, live, runErr)

	case <-superseded:
		// A newer attempt owns this work. Stop EXECUTING, not merely stop
		// renewing a lease we no longer hold: the old agent kept running while
		// its replacement ran too, which is two runtimes for one work item.
		live.cancel()
		if _, err := d.runtime.Stop(settleCtx, live.locator); err != nil {
			d.logger.Warn("dispatch: could not stop a superseded attempt",
				"run_id", a.RunID, "locator", live.locator, "error", err)
		}
		// No state is written here on purpose. This attempt no longer owns the
		// work, and writing to it is exactly what the fence refuses.
		<-runDone

	case <-abandoned:
		live.cancel()
		d.park(settleCtx, a, "cancel requested and the runtime at "+live.locator+
			" did not stop within the grace period")
		<-runDone

	case <-ctx.Done():
		live.cancel()
		<-runDone
	}
}

// confirmByProbe asks the provider whether the runtime exists yet, and keeps
// asking until it does or the attempt ends.
//
// An ERROR from the probe is never read as "not started". That is the same
// inference the start intent exists to prevent, one layer up: an unreachable
// container would otherwise look like an absent process, and the attempt would
// sit unconfirmed while its runtime ran.
func (d *Dispatcher) confirmByProbe(ctx context.Context, live *liveAttempt, confirm func(string)) {
	t := time.NewTicker(d.cfg.ConfirmPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			alive, err := d.runtime.Alive(ctx, live.locator)
			if err != nil {
				// Unknown. Keep asking; the supervisor will settle without a
				// confirmation if the run ends first, and an attempt that ran
				// without ever being confirmable is precisely what
				// reconciliation is for.
				continue
			}
			if alive {
				confirm("probe")
				return
			}
		}
	}
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

	// The runtime says what its own failure means. The dispatcher does not
	// guess, and the default for anything unclassified is reconciliation.
	//
	// This used to retry every error that was not explicitly flagged, which
	// reads "the run returned an error" as "nothing happened". An agent turn
	// can make an external change and then fail, and repeating it repeats
	// whatever it did — the one outcome a durable queue is supposed to prevent.
	switch d.runtime.Classify(a, runErr) {
	case OutcomeRetryable:
		d.finish(ctx, a, work.StateRetryWait, runErr.Error())
	case OutcomeFailed:
		d.finish(ctx, a, work.StateFailed, runErr.Error())
	case OutcomeSucceeded:
		// A runtime that reports success alongside an error is confused, and
		// the work is not the place to resolve that.
		d.park(ctx, a, "the runtime classified a failure as success: "+runErr.Error())
	default:
		d.park(ctx, a, "the runtime's outcome is unclear: "+runErr.Error())
	}
}

// finish records an attempt's outcome, and refuses to lose it quietly.
//
// A rejected transition used to be logged and dropped. What that actually left
// behind was an attempt still in a live state, still holding a slot, with its
// real outcome existing nowhere — visible only 60 seconds later when the lease
// expired and recovery parked it, and then described to an operator as an
// abandoned run rather than as the finished one it was. The end-to-end pass hit
// this on the most ordinary case there is: a run that ended before the
// confirmation probe had polled.
//
// The missing edge is fixed where it belongs, in the state machine. This is the
// backstop for the next one: an outcome that cannot be written lands in
// reconciliation immediately, carrying what it was trying to say.
func (d *Dispatcher) finish(ctx context.Context, a Assignment, to work.State, reason string) {
	err := d.store.Transition(ctx, work.TransitionRequest{
		WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation, To: to, Reason: reason,
	})
	if err == nil {
		return
	}
	d.logger.Error("dispatch: could not record the outcome",
		"work_id", a.Item.ID, "run_id", a.RunID, "to", to, "error", err)

	// Already terminal or superseded: another writer owns this work, and
	// writing again would be the fence doing its job in reverse.
	if to == work.StateNeedsReconciliation ||
		errors.Is(err, work.ErrTerminal) || errors.Is(err, work.ErrStaleGeneration) ||
		errors.Is(err, work.ErrNotBound) {
		return
	}
	if perr := d.store.Transition(ctx, work.TransitionRequest{
		WorkID: a.Item.ID, RunID: a.RunID, Generation: a.Generation,
		To:     work.StateNeedsReconciliation,
		Reason: "the outcome " + string(to) + " could not be recorded (" + err.Error() + "); it was: " + reason,
	}); perr != nil {
		d.logger.Error("dispatch: the outcome could not be recorded and the work could not be parked either",
			"work_id", a.Item.ID, "run_id", a.RunID, "to", to, "error", perr)
	}
}

func (d *Dispatcher) park(ctx context.Context, a Assignment, reason string) {
	d.finish(ctx, a, work.StateNeedsReconciliation, reason)
}

// heartbeat renews the lease while the attempt runs.
//
// A refusal means this attempt has been superseded. It calls onSuperseded so
// the supervisor can stop the RUN — stopping the heartbeat alone would leave
// the old agent executing beside its replacement.
func (d *Dispatcher) heartbeat(ctx context.Context, a Assignment, onSuperseded func()) {
	t := time.NewTicker(d.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.store.Heartbeat(ctx, a.RunID, a.Generation); err != nil {
				if errors.Is(err, work.ErrStaleGeneration) {
					d.logger.Warn("dispatch: this attempt has been superseded; stopping it",
						"run_id", a.RunID)
					onSuperseded()
					return
				}
				d.logger.Warn("dispatch: heartbeat failed", "run_id", a.RunID, "error", err)
			}
		}
	}
}

// watchCancel runs the whole cancel lifecycle: notice, signal, verify, grace,
// escalate, and finally give up — calling onAbandoned so the supervisor settles
// rather than waiting on a process that is not going to end.
//
// The previous version signalled once, ignored whether the runtime actually
// stopped, and returned. A runtime that ignored the signal was then never
// followed up, and because Run never returned, nothing settled at all.
func (d *Dispatcher) watchCancel(ctx context.Context, live *liveAttempt, onAbandoned func()) {
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
			d.enforceCancel(ctx, live, onAbandoned)
			return
		}
	}
}

// enforceCancel signals, verifies, waits out the grace, escalates once, and
// verifies again. It reports abandonment only after all of that.
func (d *Dispatcher) enforceCancel(ctx context.Context, live *liveAttempt, onAbandoned func()) {
	a := live.assignment

	stopped, err := d.runtime.Stop(ctx, live.locator)
	if err != nil {
		d.logger.Warn("dispatch: could not signal the runtime to stop",
			"run_id", a.RunID, "locator", live.locator, "error", err)
	}
	if err == nil && stopped {
		// Run will return on its own; settle handles the rest.
		return
	}

	// Grace, then one escalation. §4's shape: a signal, ten seconds, then a
	// harder stop aimed at this run's own process group.
	select {
	case <-ctx.Done():
		return
	case <-time.After(d.cfg.StopGrace):
	}

	stopped, err = d.runtime.Stop(ctx, live.locator)
	if err == nil && stopped {
		return
	}

	// One last look before giving up, because Stop reporting "still there" and
	// the runtime actually being there are different claims.
	if alive, aliveErr := d.runtime.Alive(ctx, live.locator); aliveErr == nil && !alive {
		return
	}
	d.logger.Error("dispatch: a cancelled runtime did not stop; parking for reconciliation",
		"run_id", a.RunID, "locator", live.locator)
	onAbandoned()
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
