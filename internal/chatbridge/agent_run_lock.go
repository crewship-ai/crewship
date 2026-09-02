package chatbridge

import "sync"

// AgentRunLock implements the same per-key exclusivity CAS as
// tryMarkRunStart/markRunEnd (see steer.go), generalised into its own type so
// it can be shared across packages instead of being reinvented per caller.
//
// The reason a second instance exists rather than reusing Bridge's own
// activeRuns map directly: that map is keyed by chat id and doubles as
// Steer's "is a run live in THIS chat" bookkeeping. The actual thing a
// collision corrupts is not a chat — it's the exec target inside the agent's
// container (see orchestrator.TmuxSessionName, keyed by AgentSlug alone: the
// tmux session name and every /tmp scratch file setupTmuxExec writes are
// derived purely from the agent slug, with no chat/run/mission component).
// A mention-driven assignment and a direct /assign both funnel through
// runAssignment for the SAME agent but under DIFFERENT, unrelated chat/
// mission ids (ensureMissionChat mints one synthetic chat per MISSION, not
// per agent), so keying exclusivity on chat id would not stop two such runs
// from racing each other. AgentRunLock is keyed on AgentID instead — the one
// identifier every producer of a RunAgent exec agrees on. ONE instance is
// shared, and as of #2269 these producers claim it: the chat send
// (Bridge.HandleChatMessage), /assign and @mention dispatch
// (api.AssignmentHandler.runAssignment), the agent cron
// (scheduler.Scheduler), and a routine's agent_run step
// (pipeline.OrchestratorRunner). Three producers do NOT yet: the inbound
// agent webhook route (internal/api/webhook.go), the direct agent-run route
// (internal/server/routes_agent.go) and the peer-query path
// (internal/api/query_handler.go). Each of those can still exec into a
// busy agent's tmux session and kill the live run; they are listed in
// CHANGELOG as a known limit rather than left to be discovered.
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
