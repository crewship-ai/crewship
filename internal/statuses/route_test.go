package statuses

import (
	"reflect"
	"testing"
)

// A DONE issue looked undeletable and uncancellable (#2413 item 9).
//
//	crewship issue update ENG-12 --status CANCELLED
//	  → 400 Invalid status transition from DONE to CANCELLED
//	crewship issue delete ENG-12
//	  → 400 Only BACKLOG or CANCELLED issues can be deleted
//
// Both refusals are correct and deliberate. What was wrong is the conclusion
// they invite: that there is no way to remove a mistakenly created issue once
// it reaches DONE. There is — DONE → BACKLOG is allowed, BACKLOG is deletable,
// and BACKLOG → CANCELLED is allowed too. The route existed the whole time and
// nothing surfaced it.
//
// RouteTo is what lets a refusal name that route. These tests pin the graph
// property the error messages now depend on: if someone makes DONE genuinely
// terminal later, this fails and the message must change with it.

func TestRouteTo_DoneReachesADeletableStatus(t *testing.T) {
	if got := RouteTo(ValidIssueTransitions, "DONE", "BACKLOG"); !reflect.DeepEqual(got, []string{"BACKLOG"}) {
		t.Errorf("DONE → BACKLOG = %v, want one hop — this is the reopen that makes a DONE issue disposable", got)
	}
	if got := RouteTo(ValidIssueTransitions, "DONE", "CANCELLED"); !reflect.DeepEqual(got, []string{"BACKLOG", "CANCELLED"}) {
		t.Errorf("DONE → CANCELLED = %v, want [BACKLOG CANCELLED] — refused in one step, reachable in two", got)
	}
}

func TestRouteTo_ShortestPathAndEdgeCases(t *testing.T) {
	if got := RouteTo(ValidIssueTransitions, "BACKLOG", "CANCELLED"); !reflect.DeepEqual(got, []string{"CANCELLED"}) {
		t.Errorf("BACKLOG → CANCELLED = %v, want the direct edge", got)
	}
	if got := RouteTo(ValidIssueTransitions, "DONE", "DONE"); len(got) != 0 {
		t.Errorf("a status to itself = %v, want an empty path", got)
	}
	// DUPLICATE is a deliberate terminal sink (no outgoing transitions), so
	// nothing is reachable from it. A nil route is what tells the delete
	// refusal to say "this cannot be moved" instead of inventing a route.
	if got := RouteTo(ValidIssueTransitions, "DUPLICATE", "BACKLOG"); got != nil {
		t.Errorf("DUPLICATE → BACKLOG = %v, want nil — DUPLICATE has no transitions out", got)
	}
	if got := RouteTo(ValidIssueTransitions, "NOT_A_STATUS", "BACKLOG"); got != nil {
		t.Errorf("unknown source = %v, want nil", got)
	}
}

func TestAllowedFrom_ReportsTheTableAndDoesNotAliasIt(t *testing.T) {
	got := AllowedFrom(ValidIssueTransitions, "DONE")
	if !reflect.DeepEqual(got, []string{"BACKLOG"}) {
		t.Fatalf("AllowedFrom(DONE) = %v, want [BACKLOG]", got)
	}
	// A caller must not be able to corrupt the canonical table by editing the
	// slice it was handed — this map is the single source of truth for every
	// status check in the codebase.
	got[0] = "MUTATED"
	if ValidIssueTransitions["DONE"][0] != "BACKLOG" {
		t.Error("AllowedFrom returned a slice aliasing the canonical map")
	}
	if got := AllowedFrom(ValidIssueTransitions, "DUPLICATE"); len(got) != 0 {
		t.Errorf("AllowedFrom(DUPLICATE) = %v, want empty (terminal)", got)
	}
	if got := AllowedFrom(ValidIssueTransitions, "NOT_A_STATUS"); got != nil {
		t.Errorf("AllowedFrom(unknown) = %v, want nil — distinct from a known terminal status", got)
	}
}
