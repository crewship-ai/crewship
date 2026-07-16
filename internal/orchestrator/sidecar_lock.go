package orchestrator

import "context"

// sidecarLock returns the per-container gate serializing the
// checkSidecar → decide → pkill → startSidecar sequence.
//
// #1220: multiple agents share ONE container per crew, and that sequence
// is a textbook check-then-act. Two agents dispatching at nearly the same
// moment both call checkSidecar, both observe the same health, both decide
// needStart, and both pkill and startSidecar concurrently — racing over
// the same 127.0.0.1:9119 listener. #1214 fixed the "restart when nothing
// changed" false positive and made a *single* exec's own restart wait for
// the old process to die, but it added no mutual exclusion ACROSS execs;
// the redundant-restart race is untouched by it and predates it.
//
// Modelled on snapshotLock (snapshot.go) — same shape, same rationale: N
// goroutines all pass the check before any of them acts. It is a buffered
// channel rather than a sync.Mutex so acquisition can honour ctx: the
// caller is holding a runSem slot for the whole run (orchestrator_run.go),
// so a cancelled run must be able to abandon the wait instead of pinning a
// process-wide slot behind another crew's slow startSidecar.
//
// Locks are created lazily and never deleted; the map grows by one entry
// per container the orchestrator has ever seen, bounded by container
// lifecycle (one entry per crew on a given host). Keyed on ContainerID,
// not CrewID: a container recreated after a crash gets a new ID while the
// crew ID persists, and the ID is what pkill/startSidecar actually target.
// Stale entries under a dead ID are harmless.
func (o *Orchestrator) sidecarLock(containerID string) chan struct{} {
	o.sidecarRestartMu.Lock()
	defer o.sidecarRestartMu.Unlock()
	// Lazy init: tests construct a bare &Orchestrator{} without New(), so
	// this must not depend on the constructor having run (same reason
	// agentSecretsLock does it).
	if o.sidecarRestartLocks == nil {
		o.sidecarRestartLocks = make(map[string]chan struct{})
	}
	lk, ok := o.sidecarRestartLocks[containerID]
	if !ok {
		lk = make(chan struct{}, 1)
		o.sidecarRestartLocks[containerID] = lk
	}
	return lk
}

// withSidecarLock runs fn holding the per-container sidecar gate.
//
// An empty containerID runs fn unlocked rather than keying every crew onto
// one shared "" lock, which would serialize unrelated containers. The
// sidecar block is gated on sidecarEnabled, not on a non-empty
// ContainerID, so this is defensive rather than theoretical.
//
// Returns ctx.Err() without running fn if the wait is cancelled.
func (o *Orchestrator) withSidecarLock(ctx context.Context, containerID string, fn func() error) error {
	if containerID == "" {
		return fn()
	}
	lk := o.sidecarLock(containerID)
	select {
	case lk <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lk }()
	return fn()
}

// sidecarRestartLockCount reports how many per-container gates exist. Test
// hook for the lazy-init/keying behaviour; not used in production code.
func (o *Orchestrator) sidecarRestartLockCount() int {
	o.sidecarRestartMu.Lock()
	defer o.sidecarRestartMu.Unlock()
	return len(o.sidecarRestartLocks)
}
