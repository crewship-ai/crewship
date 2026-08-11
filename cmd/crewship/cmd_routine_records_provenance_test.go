package main

// `routine records` has to say the same thing the dashboard does about WHY a
// run happened, or the two disagree about the same row.
//
// The trap is the TRIGGER column. An automation-fired run arrives with
// triggered_via="schedule" — the shared pending-run dispatcher writes that
// for every deferred run — so printing the enum verbatim tells an operator a
// cron did it when a rule did. The rule's name is on the row; the column has
// to use it.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineRecords_TriggerColumnNamesTheAutomation(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.JSONResponse(200, []runRecordRow{
		{ID: "r-auto", Status: "completed", Mode: "live",
			TriggeredVia: "schedule", AutomationName: "Triage new bugs", AutomationID: "auto-7",
			StartedAt: "2026-08-07T08:00:00Z"},
		{ID: "r-cron", Status: "completed", Mode: "live",
			TriggeredVia: "schedule", StartedAt: "2026-08-07T07:00:00Z"},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "automation") {
		t.Errorf("a rule-fired run must not be reported as a schedule:\n%s", out)
	}
	if !strings.Contains(out, "Triage new bugs") {
		t.Errorf("the rule that fired the run is not named:\n%s", out)
	}
	// The cron row must keep saying schedule — the point is the distinction,
	// not relabelling everything deferred.
	if !strings.Contains(out, "schedule") {
		t.Errorf("cron-fired run lost its trigger:\n%s", out)
	}
}

func TestRoutineRecords_ComposedRunShowsChainDepth(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.JSONResponse(200, []runRecordRow{
		{ID: "r-deep", Status: "completed", Mode: "live", TriggeredVia: "call_pipeline",
			ChainDepth: 4, ChainOrigin: "run-root", StartedAt: "2026-08-07T08:00:00Z"},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "CHAIN") {
		t.Errorf("no chain column:\n%s", out)
	}
	if !strings.Contains(out, "4") {
		t.Errorf("chain depth not printed:\n%s", out)
	}
}

// A workspace where nothing composes must not pay a column of em-dashes for
// the feature. The chain column appears only when some row is composed.
func TestRoutineRecords_NoChainColumnWhenNothingComposed(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.JSONResponse(200, []runRecordRow{
		{ID: "r1", Status: "completed", Mode: "live", TriggeredVia: "manual", StartedAt: "2026-08-07T08:00:00Z"},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "CHAIN") {
		t.Errorf("chain column drawn for a workspace with no composed runs:\n%s", out)
	}
}
