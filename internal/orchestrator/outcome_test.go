package orchestrator

// Tests for outcome.go — the §9.6 vocabulary and routing table
// (PRD-ISSUES-AND-ROUTINES-2026, work package B6, #2349).

import "testing"

func TestNormalizeOutcome(t *testing.T) {
	cases := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{"NEEDS_HUMAN", "NEEDS_HUMAN", true},
		{" needs_human \n", "NEEDS_HUMAN", true},
		{"NoChange", "NOCHANGE", false}, // no case-mangling rescue for a missing underscore
		{"NO_CHANGE", "NO_CHANGE", true},
		{"", "", false},
		{"bogus", "BOGUS", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeOutcome(tc.raw)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("NormalizeOutcome(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestValidOutcome_ExactlyTheSevenValues(t *testing.T) {
	want := map[string]bool{
		"NO_CHANGE": true, "SUCCEEDED": true, "WORK_CREATED": true, "PARTIAL": true,
		"NEEDS_HUMAN": true, "FAILED": true, "CANCELLED": true,
	}
	for v := range want {
		if !ValidOutcome(v) {
			t.Errorf("ValidOutcome(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "no_change", "SUCCESS", "DONE", "PENDING"} {
		if ValidOutcome(v) {
			t.Errorf("ValidOutcome(%q) = true, want false", v)
		}
	}
	if len(AllOutcomes) != len(want) {
		t.Errorf("AllOutcomes has %d entries, want %d", len(AllOutcomes), len(want))
	}
}

func TestReportedOutcome_ChecksBothHandoffShapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			"checkpoint block",
			"work done\n---CHECKPOINT---\ndone: x\nnext_step: y\nconfidence: high\noutcome: PARTIAL\n---END CHECKPOINT---\n",
			"PARTIAL",
		},
		{
			"handoff block",
			"work done\n---HANDOFF---\nsummary: did the thing\nconfidence: high\noutcome: WORK_CREATED\n---END HANDOFF---\n",
			"WORK_CREATED",
		},
		{
			"checkpoint present but no outcome field",
			"---CHECKPOINT---\ndone: x\nnext_step: y\nconfidence: high\n---END CHECKPOINT---\n",
			"",
		},
		{"no block at all", "just some prose, nothing structured", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReportedOutcome(tc.text); got != tc.want {
				t.Errorf("ReportedOutcome(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestReportedOutcome_ChecklistPrefersCheckpointOverHandoff(t *testing.T) {
	// A result carrying BOTH blocks (unusual, but not forbidden) — CHECKPOINT
	// wins, matching the doc comment: it's what session-bearing runs are
	// instructed to emit, and finishAssignment services both run shapes
	// through one function.
	text := "---HANDOFF---\nsummary: s\nconfidence: high\noutcome: FAILED\n---END HANDOFF---\n" +
		"---CHECKPOINT---\ndone: d\nnext_step: n\nconfidence: high\noutcome: SUCCEEDED\n---END CHECKPOINT---\n"
	if got := ReportedOutcome(text); got != "SUCCEEDED" {
		t.Errorf("ReportedOutcome = %q, want SUCCEEDED (CHECKPOINT takes precedence)", got)
	}
}

func TestDeriveOutcome_CancelledAndFailedAreNeverOverridden(t *testing.T) {
	cases := []struct {
		status   string
		reported string
	}{
		{"CANCELLED", "SUCCEEDED"},
		{"cancelled", "NEEDS_HUMAN"},
		{"FAILED", "SUCCEEDED"},
		{"failed", "NO_CHANGE"},
	}
	for _, tc := range cases {
		outcome, reason := DeriveOutcome(tc.status, tc.reported)
		want := OutcomeFailed
		if tc.status == "CANCELLED" || tc.status == "cancelled" {
			want = OutcomeCancelled
		}
		if outcome != want {
			t.Errorf("DeriveOutcome(%q, %q) = %q, want %q", tc.status, tc.reported, outcome, want)
		}
		if reason != "" {
			t.Errorf("DeriveOutcome(%q, %q) reason = %q, want empty (a self-report is never trusted here)", tc.status, tc.reported, reason)
		}
	}
}

func TestDeriveOutcome_CompletedTrustsAValidReport(t *testing.T) {
	for _, v := range AllOutcomes {
		if v == OutcomeCancelled {
			continue // not a value an agent would self-report on a clean completion
		}
		outcome, reason := DeriveOutcome("COMPLETED", v)
		if outcome != v {
			t.Errorf("DeriveOutcome(COMPLETED, %q) = %q, want %q", v, outcome, v)
		}
		if reason != "" {
			t.Errorf("DeriveOutcome(COMPLETED, %q) reason = %q, want empty", v, reason)
		}
	}
}

func TestDeriveOutcome_CompletedWithNoValidReport_DefaultsFailedWithReason(t *testing.T) {
	for _, reported := range []string{"", "bogus", "success", "done"} {
		outcome, reason := DeriveOutcome("COMPLETED", reported)
		if outcome != OutcomeFailed {
			t.Errorf("DeriveOutcome(COMPLETED, %q) = %q, want %q", reported, outcome, OutcomeFailed)
		}
		if reason != ReasonNoOutcomeReported {
			t.Errorf("DeriveOutcome(COMPLETED, %q) reason = %q, want %q", reported, reason, ReasonNoOutcomeReported)
		}
	}
}

func TestRouteForOutcome_MatchesTheSection96Table(t *testing.T) {
	cases := []struct {
		outcome          string
		wantInbox        bool
		wantSessionState string
	}{
		{OutcomeNoChange, false, "idle"},
		{OutcomeSucceeded, false, "idle"},
		{OutcomeWorkCreated, false, "idle"},
		{OutcomePartial, false, "idle"},
		{OutcomeNeedsHuman, true, "awaiting_input"},
		{OutcomeFailed, false, "error"},
		{OutcomeCancelled, false, "idle"},
	}
	for _, tc := range cases {
		r := RouteForOutcome(tc.outcome)
		if r.CreatesInboxItem != tc.wantInbox {
			t.Errorf("RouteForOutcome(%q).CreatesInboxItem = %v, want %v", tc.outcome, r.CreatesInboxItem, tc.wantInbox)
		}
		if r.SessionState != tc.wantSessionState {
			t.Errorf("RouteForOutcome(%q).SessionState = %q, want %q", tc.outcome, r.SessionState, tc.wantSessionState)
		}
	}
}

// §12's hard rule, pinned directly against the table rather than only
// through finishAssignment's integration test: NO_CHANGE and SUCCEEDED
// never create an inbox item.
func TestRouteForOutcome_NoChangeAndSucceeded_NeverCreateAnItem(t *testing.T) {
	for _, o := range []string{OutcomeNoChange, OutcomeSucceeded} {
		if RouteForOutcome(o).CreatesInboxItem {
			t.Errorf("RouteForOutcome(%q).CreatesInboxItem = true, want false (§12)", o)
		}
	}
}

func TestRouteForOutcome_UnrecognisedOutcome_FailsClosedAsFailed(t *testing.T) {
	r := RouteForOutcome("SOMETHING_UNKNOWN")
	want := RouteForOutcome(OutcomeFailed)
	if r != want {
		t.Errorf("RouteForOutcome(unknown) = %+v, want the FAILED row %+v", r, want)
	}
}
