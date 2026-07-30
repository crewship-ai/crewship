package statuses

import "testing"

// This file guards the exact bug found in the 2026-07-30 CLI/state-machine
// audit: DUPLICATE was a key in ValidIssueTransitions (so CLI --help,
// docs, and the frontend badge/board all treated it as a real issue
// status) with an empty target list, and — critically — no *other*
// status's transition list named DUPLICATE as a destination either. The
// status existed but no sequence of transitions could ever reach it, so
// any caller that followed the advertised status list got a 400 "Invalid
// status transition" with no explanation.
//
// The valuable invariant isn't "DUPLICATE specifically must be reachable"
// — it's "every status this package knows about must be reachable",
// because that's what catches the *next* status someone adds to the
// enum and forgets to wire into the transition graph. BACKLOG is the
// sole exception: it's the implicit status new issues/missions/tasks are
// created with (see internal/api issue_create_core.go), so it is reached
// via INSERT, not via a transition, and has no obligation to appear as a
// target.

// unreachableTargets returns the subset of keys in transitions that never
// appear as a target value in any of the map's entries (including their
// own, since a status transitioning only to itself would still be a
// practical dead end for every other status).
func unreachableTargets(transitions map[string][]string, exempt ...string) []string {
	isExempt := make(map[string]bool, len(exempt))
	for _, e := range exempt {
		isExempt[e] = true
	}

	reachable := make(map[string]bool)
	for _, targets := range transitions {
		for _, t := range targets {
			reachable[t] = true
		}
	}

	var orphans []string
	for status := range transitions {
		if isExempt[status] {
			continue
		}
		if !reachable[status] {
			orphans = append(orphans, status)
		}
	}
	return orphans
}

// TestValidIssueTransitions_AllStatusesReachable is the regression test for
// the DUPLICATE bug: every issue status the CLI/API/frontend can display
// or filter by (i.e. every key in ValidIssueTransitions) must be reachable
// via at least one transition, or it's a status nothing can ever legally
// enter.
func TestValidIssueTransitions_AllStatusesReachable(t *testing.T) {
	if orphans := unreachableTargets(ValidIssueTransitions, "BACKLOG"); len(orphans) > 0 {
		t.Errorf("issue status(es) offered but unreachable via any transition: %v -- "+
			"either add them as a target somewhere in ValidIssueTransitions, or stop "+
			"offering them in the CLI help text / docs / frontend status pickers", orphans)
	}
}

// Note: this file intentionally does not assert the same invariant for
// ValidMissionTransitions / ValidTaskTransitions. A pass with
// unreachableTargets over those maps turns up more orphans (e.g. DONE in
// ValidMissionTransitions, BLOCKED in ValidTaskTransitions) that may or
// may not be reachable through a non-transition path (direct INSERT,
// like BACKLOG/PENDING) -- that needs its own investigation and is out
// of scope for this issue-status fix. Flagged for follow-up rather than
// asserted here so this PR doesn't fail CI over a pre-existing condition
// nobody has decided how to resolve yet.

// TestDuplicateTransition_SpecificFix is the concrete regression test for
// the chosen fix: DUPLICATE is now reachable directly from every open
// issue status, mirroring exactly where CANCELLED is reachable from,
// since both are "close without shipping" outcomes. It remains a
// terminal sink (no outgoing transitions) -- see
// docs/api-reference/issues.mdx's Issue Statuses table.
func TestDuplicateTransition_SpecificFix(t *testing.T) {
	openStatuses := []string{"BACKLOG", "TODO", "IN_PROGRESS", "REVIEW"}
	for _, from := range openStatuses {
		if !IsValidTransition(ValidIssueTransitions, from, "DUPLICATE") {
			t.Errorf("%s -> DUPLICATE should be a valid transition (mirrors %s -> CANCELLED)", from, from)
		}
	}
	// DONE and FAILED were never able to reach CANCELLED either; DUPLICATE
	// stays consistent with that existing asymmetry rather than inventing
	// a new one.
	for _, from := range []string{"DONE", "FAILED"} {
		if IsValidTransition(ValidIssueTransitions, from, "DUPLICATE") {
			t.Errorf("%s -> DUPLICATE should not be valid (matches %s -> CANCELLED, which is also not valid)", from, from)
		}
	}
	// Still a sink: nothing transitions out of DUPLICATE.
	if len(ValidIssueTransitions["DUPLICATE"]) != 0 {
		t.Errorf("DUPLICATE should have no outgoing transitions, got %v", ValidIssueTransitions["DUPLICATE"])
	}
}
