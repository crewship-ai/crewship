package api

import (
	"database/sql"
	"testing"
)

// The chain needs a PARENT, not just a root.
//
// chain_origin says which trace a piece of work belongs to, which is what
// collapses a workflow into one row in a list. It does not say what called
// what: every assignment in a trace carries the SAME origin, so a graph built
// from it alone is a bag of nodes, not a tree.
//
// Agent → agent already has its edge (parent_assignment_id). Routine → agent
// had none: the crewship step's assignment.create passes author_run_id, the
// handler resolves it to an origin and then drops the run id itself, so nothing
// records "this run dispatched this agent". The walk could group the work and
// not draw the line — which is the one thing the picture exists to do.
//
// parent_run_id is to a routine dispatch what parent_assignment_id is to a
// delegation. Same shape, same nullability, same meaning for empty.
func TestAssignmentParentRun_RoutineDispatchRecordsTheRunThatDispatchedIt(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")

	id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))

	parent := parentRunOf(t, f, id)
	if !parent.Valid || parent.String != "run_hop_two" {
		t.Errorf("parent_run_id = %v, want %q — the origin says which trace this belongs to, "+
			"and every hop in the trace says the same thing; only this says which run "+
			"dispatched it, so without it the graph has the node and not the edge",
			parent, "run_hop_two")
	}
}

// The edge names the DISPATCHING run, not the chain root. They differ the
// moment a chain is more than one hop deep, and conflating them collapses a
// seven-hop tree into a star with every leaf hanging off the root.
func TestAssignmentParentRun_NamesTheDispatcherNotTheRoot(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")

	id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))

	parent := parentRunOf(t, f, id)
	origin := chainOriginOf(t, f, id)
	if parent.String == origin.String {
		t.Errorf("parent_run_id and chain_origin are both %q — the dispatching run is "+
			"run_hop_two and the chain root is run_the_root; if these agree the tree is a star",
			parent.String)
	}
}

// A dispatch with no run behind it — an agent assigning through the sidecar, a
// human — leaves it empty. An invented edge is worse than a missing one: the
// missing one is visible as a gap the walk can declare, the invented one is
// indistinguishable from a real edge and points at nothing.
func TestAssignmentParentRun_NoCausingRunLeavesItEmpty(t *testing.T) {
	f := setupDelegationFixture(t)

	id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "", "w1"))

	if parent := parentRunOf(t, f, id); parent.Valid && parent.String != "" {
		t.Errorf("parent_run_id = %q, want NULL — nothing dispatched this, and an edge that "+
			"reads exactly like a real one is the failure the walk's gap reporting exists to avoid",
			parent.String)
	}
}

// A delegation is the OTHER producer and keeps its own edge. Filling
// parent_run_id there too would give one row two parents and let the walk draw
// the same work twice.
func TestAssignmentParentRun_DelegationKeepsItsAssignmentEdge(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")

	parentID := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))
	holdInFlight(t, f, parentID)

	childID := createdAssignmentID(t, f.assign(t, f.work1, "w2", nil))

	if got := parentAssignmentOf(t, f, childID); !got.Valid || got.String != parentID {
		t.Errorf("parent_assignment_id = %v, want %q — the delegation edge is the one that "+
			"carries agent → agent", got, parentID)
	}
	if got := parentRunOf(t, f, childID); got.Valid && got.String != "" {
		t.Errorf("parent_run_id = %q on a delegated assignment — two parents means the walk "+
			"can reach this work by two paths and draw it twice", got.String)
	}
}

// parent_run_id has to be EARNED, exactly as chain_origin already is.
//
// The two columns come from the same field — the author_run_id on the body —
// and until now they treated it differently: chain_origin only accepted a run
// that resolved in the workspace (chainOriginForCausingRun), while parent_run_id
// was written straight through, unchecked, outside that branch. So one field
// produced a row that says "no chain I can name, but definitely dispatched by
// run X" — a claim nothing verified.
//
// It matters because of who can write it. /api/v1/internal/assignments is a
// master-token route, and the sidecar is one of its callers: an agent that names
// a run id gets that id recorded as the run that dispatched its work. The walk
// then draws an edge from that run to work it never dispatched, and the /chains
// UI presents the result as evidence of what caused what.
//
// This does NOT stop an agent naming a real run of its own workspace — the walk
// joins on workspace_id, and inside one tenant this is a provenance problem
// rather than an isolation one. What it stops is the half that is checkable: an
// id that names nothing here, which is either invented or somebody else's.
func TestAssignmentParentRun_AnUnprovenCausingRunIsNotRecordedAsTheDispatcher(t *testing.T) {
	t.Run("a run id that names nothing", func(t *testing.T) {
		f := setupDelegationFixture(t)

		id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_i_made_up", "w1"))

		if parent := parentRunOf(t, f, id); parent.Valid && parent.String != "" {
			t.Errorf("parent_run_id = %q, want NULL — nothing in this workspace answers to that id, "+
				"so the edge points at a node the walk cannot reach and reads as evidence anyway",
				parent.String)
		}
	})

	t.Run("a real run, in another tenant", func(t *testing.T) {
		f := setupDelegationFixture(t)
		if _, err := f.db.Exec(
			`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other-parent', 'Other', 'other-parent')`); err != nil {
			t.Fatalf("seed other workspace: %v", err)
		}
		seedChainRun(t, f.db, "ws-other-parent", "run_of_another_tenant", "run_their_root")

		id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_of_another_tenant", "w1"))

		// chain_origin already refuses this run (its own test one file over). The
		// row must not disagree with itself by refusing the chain and then
		// recording the same run as the dispatcher.
		if origin := chainOriginOf(t, f, id); origin.Valid {
			t.Fatalf("precondition: chain_origin = %q, want NULL", origin.String)
		}
		if parent := parentRunOf(t, f, id); parent.Valid && parent.String != "" {
			t.Errorf("parent_run_id = %q — the same run was refused as the chain and accepted as "+
				"the dispatcher, so one row makes two contradictory claims about one id",
				parent.String)
		}
	})
}

// The other half of the same resolution: a run that DOES prove out still does
// not win against the delegation tree. This is the case that used to be
// unreachable — the caller could only get here with an unvalidated id or none —
// so dispatchedBy's "one parent only" guard had nothing to refuse and could not
// be told from a guard that does nothing.
func TestAssignmentParentRun_DelegatedCallerNamingAValidRunStillKeepsOneParent(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")
	seedChainRun(t, f.db, f.wsID, "run_unrelated", "run_some_other_root")

	parentID := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))
	holdInFlight(t, f, parentID)

	// w1 is now executing an assignment of its own AND names a run that really
	// does resolve in this workspace.
	childID := createdAssignmentID(t, f.routineDispatch(t, f.work1, "run_unrelated", "w2"))

	if got := parentAssignmentOf(t, f, childID); !got.Valid || got.String != parentID {
		t.Fatalf("precondition: parent_assignment_id = %v, want %q — without the delegation edge "+
			"this test is not exercising the two-parents case at all", got, parentID)
	}
	if got := parentRunOf(t, f, childID); got.Valid && got.String != "" {
		t.Errorf("parent_run_id = %q alongside parent_assignment_id %q — a row with two parents "+
			"can be reached by two paths and drawn twice; validating the run does not make it "+
			"the parent", got.String, parentID)
	}
}

func parentRunOf(t *testing.T, f *delegationFixture, assignmentID string) sql.NullString {
	t.Helper()
	var parent sql.NullString
	if err := f.db.QueryRow(
		`SELECT parent_run_id FROM assignments WHERE id = ?`, assignmentID).Scan(&parent); err != nil {
		t.Fatalf("read parent_run_id of %s: %v", assignmentID, err)
	}
	return parent
}
