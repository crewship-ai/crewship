package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestFormatDurMs(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{500, "500ms"},
		{999, "999ms"},
		{1500, "1.5s"},
		{59999, "60.0s"},
		{60000, "1m0s"},
		{125000, "2m5s"},
	}
	for _, tc := range cases {
		if got := formatDurMs(tc.ms); got != tc.want {
			t.Errorf("formatDurMs(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

const covRecordsPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipelines/daily-report/run-records"

func TestRoutineRecordsRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.JSONResponse(200, []runRecordRow{
		{ID: "crun567890abcdefghijklm", PipelineSlug: "daily-report", Status: "completed", Mode: "live",
			TriggeredVia: "schedule", DurationMs: 65000, CostUSD: 0.0123, StartedAt: "2026-06-10T08:00:00Z"},
		{ID: "crun567890abcdefghijkln", PipelineSlug: "daily-report", Status: "failed", Mode: "live",
			TriggeredVia: "manual", DurationMs: 0, CostUSD: 0, StartedAt: "2026-06-09T08:00:00Z"},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "RUN ID") || !strings.Contains(out, "completed") || !strings.Contains(out, "failed") {
		t.Errorf("table rows missing:\n%s", out)
	}
	if !strings.Contains(out, "1m5s") || !strings.Contains(out, "$0.0123") {
		t.Errorf("duration/cost formatting missing:\n%s", out)
	}
	// Zero duration and zero cost render the em-dash placeholder.
	if !strings.Contains(out, "—") {
		t.Errorf("zero values should render —:\n%s", out)
	}
	calls := s.CallsFor("GET", covRecordsPath)
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=50") {
		t.Errorf("default limit not propagated: %+v", calls)
	}
}

func TestRoutineRecordsRunE_JSONAndStatusFilter(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.JSONResponse(200, []runRecordRow{
		{ID: "r1", Status: "failed", TriggeredVia: "manual"},
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, routineRecordsCmd, "status", "failed")
	setFlagCovCli10(t, routineRecordsCmd, "json", "true")
	setFlagCovCli10(t, routineRecordsCmd, "limit", "700") // out of range → clamps to 50

	out, err := captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"status": "failed"`) {
		t.Errorf("json output missing row:\n%s", out)
	}
	calls := s.CallsFor("GET", covRecordsPath)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "status=failed") || !strings.Contains(calls[0].Query, "limit=50") {
		t.Errorf("query wrong: %q", calls[0].Query)
	}
}

func TestRoutineRecordsRunE_EmptyVariants(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.JSONResponse(200, []runRecordRow{}))
	covSetupCli10(t, s.URL())

	// No filter → "No runs yet" + trigger hint.
	out, err := captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No runs yet") || !strings.Contains(out, "crewship routine run daily-report") {
		t.Errorf("empty message missing:\n%s", out)
	}

	// With status filter → status-specific message.
	setFlagCovCli10(t, routineRecordsCmd, "status", "running")
	out, err = captureStdoutCovCli10(t, func() error {
		return routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	})
	if err != nil {
		t.Fatalf("RunE filtered: %v", err)
	}
	if !strings.Contains(out, "No running runs") {
		t.Errorf("filtered empty message missing:\n%s", out)
	}
}

func TestRoutineRecordsRunE_LegacyServer503(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.ErrorResponse(503, "no run store"))
	covSetupCli10(t, s.URL())

	err := routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	if err == nil || !strings.Contains(err.Error(), "predates migration v83") {
		t.Errorf("expected legacy fallback hint, got %v", err)
	}
}

func TestRoutineRecordsRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestRoutineRecordsRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covRecordsPath, clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := routineRecordsCmd.RunE(routineRecordsCmd, []string{"daily-report"}); err == nil {
		t.Error("expected error from 500")
	}
}
