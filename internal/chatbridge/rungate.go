package chatbridge

import "sync"

// RunGate implements the same per-key exclusivity CAS as
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
// from racing each other. RunGate is keyed on AgentID instead — the one
// identifier every producer of a RunAgent exec (chat send, /assign, an
// @mention dispatch) agrees on — and one instance is shared between
// chatbridge.Bridge and api.AssignmentHandler (see Bridge.RunGate /
// AssignmentHandler.SetRunGate) so both doors that can start a live exec for
// an agent claim the same slot.
type RunGate struct {
	mu     sync.Mutex
	active map[string]int
}

// NewRunGate returns an empty RunGate.
func NewRunGate() *RunGate {
	return &RunGate{active: make(map[string]int)}
}

// TryStart atomically claims the run slot for key: it succeeds (and marks a
// run started) only if no run is currently active for this key; otherwise it
// leaves the counter untouched and reports failure. Counter, not bool, so a
// caller that (legitimately, elsewhere) allows overlapping claims for the
// same key doesn't have one finishing run clear the slot out from under
// another still-live one — mirrors markRunStart/markRunEnd's rationale.
func (g *RunGate) TryStart(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[key] > 0 {
		return false
	}
	g.active[key]++
	return true
}

// End releases one claim on key, deleting the entry at zero so the map
// doesn't grow unbounded. Guards against underflow so a stray extra call
// can never wedge a key permanently "busy".
func (g *RunGate) End(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[key] <= 1 {
		delete(g.active, key)
		return
	}
	g.active[key]--
}

// InFlight reports whether at least one run is currently claimed for key.
func (g *RunGate) InFlight(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[key] > 0
}
