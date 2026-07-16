package orchestrator

import "sync"

// lockSidecarLifecycle serializes the sidecar check→decide→pkill→start
// sequence per container (#1220). Multiple agents share ONE Docker container
// per crew, and without this lock two execs dispatching at nearly the same
// moment could both run checkSidecar, both observe the same health state, both
// decide a (re)start is needed, and both pkill + startSidecar — one killing
// the other's freshly started sidecar, or double-starting it. #1214 fixed the
// single-exec restart ordering (wait for the old process to exit) but
// explicitly deferred this cross-exec mutual exclusion.
//
// Keyed by containerID so concurrent execs against DIFFERENT containers are
// unaffected — no global lock. Lock entries are one *sync.Mutex per container
// for the process lifetime (same footprint rationale as tmuxCache /
// staleSidecarJournaled: containers are few and long-lived, and a stale entry
// for a recreated container is harmless — the new container ID gets its own
// slot).
//
// Deadlock posture: callers must NOT hold o.mu when acquiring, and the locked
// region must not acquire o.mu.Lock (RunAgent's sidecar block only reads
// snapshot-copied config and uses nil-safe helpers). The lock IS held across
// container execs (health probe, pkill wait, sidecar start) — that is the
// point: the decision is only valid if the state it was based on can't be
// mutated mid-sequence. Each of those execs is bounded (execPreflight timeout,
// startSidecar's in-script health deadline), so the hold time is bounded too.
//
// Returns an idempotent unlock so callers can release early on the happy path
// and still `defer` it for error returns. Zero-value sidecarLifecycleLocks
// works, so bare &Orchestrator{} tests need no constructor change.
func (o *Orchestrator) lockSidecarLifecycle(containerID string) func() {
	muAny, _ := o.sidecarLifecycleLocks.LoadOrStore(containerID, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	var once sync.Once
	return func() { once.Do(mu.Unlock) }
}
