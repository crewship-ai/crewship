package main

import "testing"

// TestCheckScheduleCircuitBreaker_DraftIsNotReportedHealthy is the
// code-review fix for B8 (#2359): before this fix, checkScheduleCircuitBreaker
// had no branch for a draft trigger (enabled=false, no disabled_reason, 0
// consecutive_failures) and fell through to the "healthy" default —
// `routine doctor` would tell an operator a routine awaiting MANAGER
// approval was fine.
func TestCheckScheduleCircuitBreaker_DraftIsNotReportedHealthy(t *testing.T) {
	body := `[{"id":"psched_1","name":"nightly","target_pipeline_slug":"my-routine",
		"cron_expr":"0 9 * * *","enabled":false,"activation":"draft",
		"consecutive_failures":0,"max_consecutive_failures":5}]`
	client := &fakeDoctorGetter{status: 200, body: body}

	checks := checkScheduleCircuitBreaker(client, "ws_test", "my-routine")
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d: %+v", len(checks), checks)
	}
	if checks[0].Level != doctorWarn {
		t.Fatalf("expected a WARN for a draft schedule, got %v (%s)", checks[0].Level, checks[0].Message)
	}
	if checks[0].Message == "schedule \"nightly\" healthy (0 consecutive failures)" {
		t.Fatalf("draft schedule must not be reported healthy: %s", checks[0].Message)
	}
}

// TestEnabledCell_DraftReportsAwaitingActivation proves the CLI list
// column names the draft state distinctly from an ordinary disable.
func TestEnabledCell_DraftReportsAwaitingActivation(t *testing.T) {
	got := enabledCell(scheduleRow{Enabled: false, Activation: "draft"})
	if got != "no (awaiting activation)" {
		t.Fatalf("enabledCell(draft) = %q, want %q", got, "no (awaiting activation)")
	}
	// An ordinary disable is unaffected.
	got = enabledCell(scheduleRow{Enabled: false, DisabledReason: "circuit_breaker"})
	if got != "no (circuit_breaker)" {
		t.Fatalf("enabledCell(circuit_breaker) = %q, want %q", got, "no (circuit_breaker)")
	}
}
