package sidecar

import (
	"sync"
	"time"
)

// Per-run identity state — E0.
//
// The sidecar's roster (IPCConfig.AgentToken + crewMembers) is minted once, by
// whichever run happened to start the sidecar, and never refreshed: the restart
// predicate (orchestrator's sidecarNeedsRestart) looks at the credential set,
// the network mode and the egress domains, and at nothing about who is running.
// That is fine for a roster of AGENTS, which changes rarely — and impossible
// for a roster of RUNS, which changes constantly.
//
// So runs are not rostered. A run token verifies against the crew's run key
// (internaltoken.ValidateAgentRunToken), and this registry answers only the
// question a MAC cannot: is that run still current?
//
// The default for an UNKNOWN run is ACCEPT. A run whose start notification was
// lost, or one that began before this sidecar booted, is a run whose token is
// cryptographically sound and whose work is legitimate; refusing it would make
// a dropped control message look exactly like a forged token. What this
// registry exists to refuse is the case that is not ambiguous: a run the
// orchestrator has explicitly told us has ENDED. That is the "reject a token
// whose run is no longer current" requirement, and it is the one a late or
// zombie runtime trips (T08).
type runRegistry struct {
	mu   sync.RWMutex
	runs map[string]*runState
}

// runState is what the sidecar knows about one run beyond what its token
// proves. ChatID is the interesting field: it is the per-request context that
// replaces the boot-frozen s.ipc.ChatID on every route that stamps a chat onto
// an escalation, a peer query or an issue.
type runState struct {
	AgentID   string
	AgentSlug string
	ChatID    string
	MissionID string
	StartedAt time.Time
	EndedAt   time.Time // zero while live
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[string]*runState)}
}

// start records a run as live. Idempotent: the orchestrator may retry, and a
// repeat must not resurrect a run that has since ended — re-starting an ended
// run id would be indistinguishable from a replay of a stale notification.
func (r *runRegistry) start(runID string, st runState) {
	if runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.runs[runID]; ok && !existing.EndedAt.IsZero() {
		return
	}
	st.StartedAt = time.Now()
	r.runs[runID] = &st
}

// end marks a run finished. Recorded even for a run this sidecar never saw
// start: the end notification is the authoritative one, and a run that started
// before this sidecar booted must still become un-current when it finishes.
func (r *runRegistry) end(runID string) {
	if runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.runs[runID]
	if !ok {
		st = &runState{}
		r.runs[runID] = st
	}
	if st.EndedAt.IsZero() {
		st.EndedAt = time.Now()
	}
}

// current reports whether runID may still act. Unknown runs are current — see
// the type comment for why the default is accept and not refuse.
func (r *runRegistry) current(runID string) bool {
	if runID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.runs[runID]
	if !ok {
		return true
	}
	return st.EndedAt.IsZero()
}

// lookup returns what is known about a run, and whether anything is.
func (r *runRegistry) lookup(runID string) (runState, bool) {
	if runID == "" {
		return runState{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.runs[runID]
	if !ok {
		return runState{}, false
	}
	return *st, true
}

// runRegistryMaxEntries caps the map. A sidecar lives as long as its crew
// container, which on a busy workspace can be days, and every run leaves an
// entry — so without a cap this grows without bound for the same reason the
// orchestrator's tmux cache needed one.
const runRegistryMaxEntries = 4096

// sweep drops ended runs once the map grows past the cap, oldest-ended first.
// LIVE runs are never dropped: forgetting a live run would only downgrade it to
// "unknown", which is still accepted, but forgetting its ChatID would silently
// re-point its escalations at the boot chat — the exact bug per-run context
// exists to fix.
func (r *runRegistry) sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) <= runRegistryMaxEntries {
		return
	}
	var oldest string
	var oldestAt time.Time
	for len(r.runs) > runRegistryMaxEntries {
		oldest, oldestAt = "", time.Time{}
		for id, st := range r.runs {
			if st.EndedAt.IsZero() {
				continue
			}
			if oldest == "" || st.EndedAt.Before(oldestAt) {
				oldest, oldestAt = id, st.EndedAt
			}
		}
		if oldest == "" {
			return // everything left is live; nothing safe to drop
		}
		delete(r.runs, oldest)
	}
}

// runEndPath is the loopback endpoint that marks a run finished.
//
// It is a ROUTE, not a push from crewshipd, because there is no inbound
// channel: the sidecar listens on 127.0.0.1:9119 INSIDE the crew container and
// crewshipd runs outside it. Every existing crewshipd→container interaction is
// an exec, and this is one too — the orchestrator delivers a one-line curl on
// an exec's stdin at run end (notifyRunEnded, internal/orchestrator).
// Under /agent rather than a namespace of its own because
// llmroute.ReservedPathSegments() is the list of top-level segments a provider
// route may not shadow, and "agent" is already on it. A fresh segment would
// have to be added there — in a package this change does not own — and until it
// was, a provider prefix could shadow the route that ends runs.
//
// Kept in step with the literal in buildHandler's switch by
// TestRunEndPathMatchesTheRoute.
const runEndPath = "/agent/run/end"
