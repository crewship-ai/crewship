package api

// The refusals a DONE issue hits are correct; the messages were not (#2413
// item 9). Both told the caller what was not allowed and nothing about what
// is, and for a mistakenly created DONE issue the natural reading was "there
// is no way to remove this". There is: DONE → BACKLOG → delete.
//
// These are unit tests on the message builders rather than handler tests: the
// refusal PATHS are already covered (issue_handler_test.go), and what changed
// here is the wording a human acts on.

import (
	"strings"
	"testing"
)

func TestIssueTransitionRefusal_NamesTheAllowedTargetsAndTheRoute(t *testing.T) {
	msg := issueTransitionRefusal("DONE", "CANCELLED")

	// The historical sentence stays at the front: it is what tests, scripts
	// and the docs all quote.
	if !strings.HasPrefix(msg, "Invalid status transition from DONE to CANCELLED") {
		t.Errorf("the original wording must lead the message, got:\n%s", msg)
	}
	if !strings.Contains(msg, "From DONE you can go to: BACKLOG") {
		t.Errorf("the refusal must say what IS allowed:\n%s", msg)
	}
	if !strings.Contains(msg, "To reach CANCELLED, go via BACKLOG") {
		t.Errorf("a two-hop route exists and must be named:\n%s", msg)
	}
}

func TestIssueTransitionRefusal_TerminalStatusSaysSo(t *testing.T) {
	// DUPLICATE is a deliberate sink. Offering a route that does not exist
	// would be worse than the bare refusal.
	msg := issueTransitionRefusal("DUPLICATE", "BACKLOG")
	if !strings.Contains(msg, "DUPLICATE is terminal") {
		t.Errorf("a terminal status must be named as terminal:\n%s", msg)
	}
	if strings.Contains(msg, "go via") {
		t.Errorf("no route exists from DUPLICATE; none must be suggested:\n%s", msg)
	}
}

func TestIssueDeleteRefusal_GivesTheCommandsThatGetRidOfTheIssue(t *testing.T) {
	msg := issueDeleteRefusal("ENG-12", "DONE")

	if !strings.Contains(msg, "ENG-12 is DONE") {
		t.Errorf("the message must name the status that blocked the delete:\n%s", msg)
	}
	// One hop, not two: BACKLOG is itself deletable, so telling the operator
	// to cancel as well would be busywork.
	if !strings.Contains(msg, "crewship issue update ENG-12 --status BACKLOG") {
		t.Errorf("missing the reopen step:\n%s", msg)
	}
	if strings.Contains(msg, "--status CANCELLED") {
		t.Errorf("BACKLOG is already deletable; the extra cancel is noise:\n%s", msg)
	}
	if !strings.Contains(msg, "crewship issue delete ENG-12") {
		t.Errorf("missing the delete that follows:\n%s", msg)
	}
}

func TestIssueDeleteRefusal_InProgressRoutesThroughCancelled(t *testing.T) {
	// IN_PROGRESS can be cancelled directly, and cancelling is the honest
	// thing to do with work that was actually started.
	msg := issueDeleteRefusal("ENG-13", "IN_PROGRESS")
	if !strings.Contains(msg, "crewship issue update ENG-13 --status CANCELLED") {
		t.Errorf("expected the direct cancel route:\n%s", msg)
	}
}

func TestIssueDeleteRefusal_TerminalStatusAdmitsThereIsNoRoute(t *testing.T) {
	msg := issueDeleteRefusal("ENG-14", "DUPLICATE")
	if !strings.Contains(msg, "no transitions out") {
		t.Errorf("a genuine dead end must be stated as one rather than dressed up:\n%s", msg)
	}
	if strings.Contains(msg, "crewship issue update") {
		t.Errorf("no route exists; no command must be offered:\n%s", msg)
	}
}
