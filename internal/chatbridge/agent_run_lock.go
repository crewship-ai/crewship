package chatbridge

import "sync"

// AgentRunLock implements the same per-key exclusivity CAS as
// tryMarkRunStart/markRunEnd (see steer.go), generalised into its own type so
// it can be shared across packages instead of being reinvented per caller.
//
// This producer-side lock preserves the existing busy/queue behavior for
// chat, assignments, scheduler and routine agent steps. It is keyed by AgentID,
// whereas Bridge's activeRuns map tracks the active turn of a particular chat.
// Webhooks, direct runs and peer queries do not acquire this lock. RunAgent now
// enforces the serial profile for all callers at the common runtime boundary,
// including these producers, and retains its reservation for detached runtimes.
// Neither process-local lock supplies durable shared admission or a mailbox;
// migration of the producer queues remains tracked in #2643.
//
// Named a lock, not a gate: it is a lock ON AN AGENT (keyed by agent id),
// held for the duration of one live RunAgent exec — "lock" says what it is
// more precisely than "gate", and keeps it out of grep range of the
// unrelated pipeline integrations gate (pipeline.ErrTestRunGateFailed and
// its TestRunGate_* tests in internal/api/pipeline_integrations_gate_test.go),
// which is a different, pre-existing concept.
type AgentRunLock struct {
	mu     sync.Mutex
	active map[string]int
}

// NewAgentRunLock returns an empty AgentRunLock.
func NewAgentRunLock() *AgentRunLock {
	return &AgentRunLock{active: make(map[string]int)}
}

// TryStart atomically claims the run slot for key: it succeeds (and marks a
// run started) only if no run is currently active for this key; otherwise it
// leaves the counter untouched and reports failure. Counter, not bool, so a
// caller that (legitimately, elsewhere) allows overlapping claims for the
// same key doesn't have one finishing run clear the slot out from under
// another still-live one — mirrors markRunStart/markRunEnd's rationale.
func (l *AgentRunLock) TryStart(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] > 0 {
		return false
	}
	l.active[key]++
	return true
}

// End releases one claim on key, deleting the entry at zero so the map
// doesn't grow unbounded. Guards against underflow so a stray extra call
// can never wedge a key permanently "busy".
func (l *AgentRunLock) End(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] <= 1 {
		delete(l.active, key)
		return
	}
	l.active[key]--
}

// InFlight reports whether at least one run is currently claimed for key.
func (l *AgentRunLock) InFlight(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active[key] > 0
}
