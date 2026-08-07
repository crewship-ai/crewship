package chain

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// The automation edge
//
// This package's rule is "a link is walked only if a column actually carries
// it, and where the schema does not carry one we SAY so (Graph.Gaps,
// Node.Partial) rather than infer it. An invented edge is worse than an
// admitted gap."
//
// automation → run is neither walked nor admitted. The column carries it:
// pipeline_runs.triggered_via = 'automation' with triggered_by_id holding the
// automations.id, which the automation registry stamps on every deferred run
// it parks. resolveAnchor's comment explains the omission — "there is no
// automations table on this branch" — and that was true of the worktree it was
// written in. The table is here now, so the premise died in the merge and took
// with it the one causal edge this whole substrate exists to draw: an operator
// asking "why did this run happen" gets a run with no parent and no gap
// telling them one is missing.
// ---------------------------------------------------------------------------

// seedAutomation inserts a live rule pointing at slug. Direct SQL for the same
// reason every other fixture here is: the point is which columns the walker
// joins on.
func (r *rig) seedAutomation(t *testing.T, id, wsID, name, eventType, routineSlug string) string {
	t.Helper()
	r.exec(t, `
INSERT INTO automations (id, workspace_id, name, enabled, event_type, matcher_json,
                         action_kind, action_config_json, debounce_seconds, max_per_hour,
                         created_at, updated_at)
VALUES (?, ?, ?, 1, ?, '{}', 'routine', ?, 10, 60, '2026-08-07T12:00:00Z', '2026-08-07T12:00:00Z')`,
		id, wsID, name, eventType, `{"routine_slug":"`+routineSlug+`"}`)
	return id
}

// nodeDepth returns the depth the walk recorded for id, or -1 if the node is
// absent. Depth is asserted rather than mere presence because a rule is also
// reachable the long way round — run → its routine → the rules that target
// that routine — and a test satisfied by that path would stay green with the
// direct triggered_via='automation' hop deleted, which is the hop being
// tested.
func nodeDepth(g *Graph, id string) int {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n.Depth
		}
	}
	return -1
}

// The flagship chain, walked upward from the run: a rule fired it, and the
// answer to "why did this run happen" is that rule — one hop up, from the
// run's own triggered_by_id.
func TestWalk_RunToTheAutomationThatFiredIt(t *testing.T) {
	r := newRig(t, "ws-auto")
	r.seedRoutine(t, "p1", r.ws, "triage")
	aut := r.seedAutomation(t, "aut_1", r.ws, "close triage", "mission.status_change", "triage")
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", aut)

	// MaxDepth 1: only what the run itself points at. The routine detour needs
	// two hops, so nothing here can reach the rule except the direct edge.
	g := walk(t, r, "run-1", Options{MaxDepth: 1, MaxNodes: 100})

	if !hasNode(g, "automation:"+aut) {
		t.Fatalf("the automation that fired this run is not adjacent to it; nodes: %#v", g.Nodes)
	}
	if got := nodeDepth(g, "automation:"+aut); got != 1 {
		t.Fatalf("automation depth = %d, want 1 — 'why did this run happen' must be one hop "+
			"from the run, not a detour through its routine", got)
	}
	if !hasEdge(g, "automation:"+aut, "run:run-1", EdgeTriggers) {
		t.Fatalf("missing automation -> run edge (pipeline_runs.triggered_via='automation'); edges: %#v", g.Edges)
	}
}

// And downward from the rule: what has this automation actually set off?
func TestWalk_AutomationAnchorReachesItsRunsAndItsRoutine(t *testing.T) {
	r := newRig(t, "ws-auto2")
	r.seedRoutine(t, "p1", r.ws, "triage")
	aut := r.seedAutomation(t, "aut_1", r.ws, "close triage", "mission.status_change", "triage")
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", aut)
	r.seedRun(t, "run-2", r.ws, "p1", "triage", "automation", aut)

	g := walk(t, r, aut, Options{MaxDepth: 3, MaxNodes: 100})

	if g.AnchorNode != "automation:"+aut {
		t.Fatalf("anchor resolved to %q, want the automation", g.AnchorNode)
	}
	for _, run := range []string{"run-1", "run-2"} {
		if !hasEdge(g, "automation:"+aut, "run:"+run, EdgeTriggers) {
			t.Errorf("missing automation -> %s edge; edges: %#v", run, g.Edges)
		}
	}
	// The rule names a routine by slug; that binding is what an operator
	// reading the rule wants next.
	if !hasEdge(g, "automation:"+aut, "routine:p1", EdgeTriggers) {
		t.Errorf("missing automation -> routine edge (action_config_json.routine_slug); edges: %#v", g.Edges)
	}
}

// Direction is a property of the RELATIONSHIP, not of which end the walk began
// at — the reason `neighbour` carries its own From/To. So the automation ->
// routine edge has to be discoverable from the ROUTINE too, or the same chain
// anchored on the rule and anchored on the routine are two different graphs.
func TestWalk_RoutineReachesTheAutomationsThatStartIt(t *testing.T) {
	r := newRig(t, "ws-auto-sym")
	r.seedRoutine(t, "p1", r.ws, "triage")
	aut := r.seedAutomation(t, "aut_1", r.ws, "close triage", "mission.status_change", "triage")

	// Anchored on the ROUTINE, one hop. The rule has never fired, so there is
	// no run to reach it through: only the definition-level edge can.
	g := walk(t, r, "p1", Options{MaxDepth: 1, MaxNodes: 100})

	if !hasEdge(g, "automation:"+aut, "routine:p1", EdgeTriggers) {
		t.Fatalf("a routine cannot see the rules that start it; edges: %#v", g.Edges)
	}

	// And the same edge, in the same direction, from the other end.
	back := walk(t, r, aut, Options{MaxDepth: 1, MaxNodes: 100})
	if !hasEdge(back, "automation:"+aut, "routine:p1", EdgeTriggers) {
		t.Fatalf("the edge flips direction depending on the anchor; edges: %#v", back.Edges)
	}
}

// The tenant fence, on the new hops. Every other expansion in this package is
// workspace-scoped in the predicate rather than by trusting the entry point,
// because triggered_by_id is an untyped string that can collide across
// tenants — an automation id from another workspace must not be dereferenced.
func TestWalk_AutomationEdgeIsWorkspaceScoped(t *testing.T) {
	r := newRig(t, "ws-auto-a")
	r.seedWorkspace(t, "ws-auto-b", "ws-auto-b-slug")
	r.seedRoutine(t, "p-b", "ws-auto-b", "triage")
	foreign := r.seedAutomation(t, "aut_foreign", "ws-auto-b", "theirs", "mission.status_change", "triage")

	r.seedRoutine(t, "p-a", r.ws, "triage")
	// A run in ws-auto-a whose triggered_by_id names ws-auto-b's rule.
	r.seedRun(t, "run-1", r.ws, "p-a", "triage", "automation", foreign)

	g := walk(t, r, "run-1", Options{MaxDepth: 3, MaxNodes: 100})

	if hasNode(g, "automation:"+foreign) {
		t.Fatalf("a foreign workspace's automation was pulled into the graph; nodes: %#v", g.Nodes)
	}
}

// A foreign automation id must be a 404, indistinguishable from one that does
// not exist — the same rule ErrAnchorNotFound already applies to every other
// anchor kind, so the endpoint cannot be used to probe for ids in a tenant the
// caller cannot read.
func TestWalk_ForeignAutomationAnchorIsNotFound(t *testing.T) {
	r := newRig(t, "ws-auto-c")
	r.seedWorkspace(t, "ws-auto-d", "ws-auto-d-slug")
	r.seedRoutine(t, "p-b", "ws-auto-d", "triage")
	foreign := r.seedAutomation(t, "aut_foreign", "ws-auto-d", "theirs", "mission.status_change", "triage")

	_, err := Walk(context.Background(), r.db, r.ws, foreign, Options{})
	if !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("err = %v, want ErrAnchorNotFound (a foreign automation must be "+
			"indistinguishable from a missing one)", err)
	}
}

// A soft-deleted rule still has to explain the runs it caused — that is the
// stated reason automations are soft-deleted at all ("the row stays so a run
// it caused can still explain where it came from", AutomationHandler.Delete).
func TestWalk_SoftDeletedAutomationStillExplainsItsRuns(t *testing.T) {
	r := newRig(t, "ws-auto-e")
	r.seedRoutine(t, "p1", r.ws, "triage")
	aut := r.seedAutomation(t, "aut_1", r.ws, "gone", "mission.status_change", "triage")
	r.exec(t, `UPDATE automations SET deleted_at = '2026-08-07T13:00:00Z' WHERE id = ?`, aut)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", aut)

	g := walk(t, r, "run-1", Options{MaxDepth: 1, MaxNodes: 100})

	if !hasEdge(g, "automation:"+aut, "run:run-1", EdgeTriggers) {
		t.Fatalf("a deleted rule stopped explaining the run it fired; edges: %#v", g.Edges)
	}
	for _, n := range g.Nodes {
		if n.ID == "automation:"+aut && n.Status != "deleted" {
			t.Fatalf("a deleted rule reads as %q; the status is how a client tells "+
				"'this rule still exists' from 'this rule is gone but caused it'", n.Status)
		}
	}
}
