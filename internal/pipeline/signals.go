package pipeline

import "sync"

// SignalRegistry is the in-process delivery channel for run signals
// (Wave 4.3 input-stream injection). A `wait` step of kind `event`
// registers a waiter keyed by (runID, eventType) and blocks; an external
// caller delivers a payload via POST /pipeline-runs/{id}/signal, which
// routes through Signal() to wake the waiter.
//
// In-memory + process-local (like the wait:datetime timer): the run
// holds its goroutine while waiting, so signals steer a LIVE run. It does
// not survive a restart — consistent with the blocking wait model, and
// appropriate for "steer a running run" (the run is by definition live).
// A single shared registry is wired at boot and injected into every
// executor.
type SignalRegistry struct {
	mu      sync.Mutex
	waiters map[string]chan string
}

// NewSignalRegistry builds an empty registry.
func NewSignalRegistry() *SignalRegistry {
	return &SignalRegistry{waiters: make(map[string]chan string)}
}

func signalKey(runID, eventType string) string { return runID + "\x00" + eventType }

// Register creates (or returns the existing) waiter channel for a
// (run, event) and a cleanup func. The channel is buffered(1) so a
// Signal that arrives a hair before the receiver parks isn't lost.
func (r *SignalRegistry) Register(runID, eventType string) (<-chan string, func()) {
	if r == nil {
		return nil, func() {}
	}
	key := signalKey(runID, eventType)
	r.mu.Lock()
	ch, ok := r.waiters[key]
	if !ok {
		ch = make(chan string, 1)
		r.waiters[key] = ch
	}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.waiters, key)
		r.mu.Unlock()
	}
}

// Signal delivers payload to the waiter for (run, event). Returns true if
// a waiter was present. Non-blocking: a full buffer (duplicate signal
// before the first was consumed) drops the extra rather than blocking the
// caller.
func (r *SignalRegistry) Signal(runID, eventType, payload string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	ch, ok := r.waiters[signalKey(runID, eventType)]
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- payload:
		return true
	default:
		return true // a signal is already queued; the run will see one
	}
}
