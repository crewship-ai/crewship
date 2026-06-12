package main

// Coverage tests for cmd_routine_logs.go — the slug-free pipeline-run
// state lookup, the slug-scoped journal timeline, and parseTime.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestParseTime(t *testing.T) {
	nano := "2026-06-10T12:34:56.789012345Z"
	if got := parseTime(nano); got.IsZero() || got.Nanosecond() == 0 {
		t.Errorf("parseTime(%q) = %v; want nano-precision time", nano, got)
	}
	plain := "2026-06-10T12:34:56Z"
	want := time.Date(2026, 6, 10, 12, 34, 56, 0, time.UTC)
	if got := parseTime(plain); !got.Equal(want) {
		t.Errorf("parseTime(%q) = %v want %v", plain, got, want)
	}
	if got := parseTime("garbage"); !got.IsZero() {
		t.Errorf("parseTime(garbage) = %v; want zero time", got)
	}
}

func TestRoutineLogsRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func covPipelineRun() map[string]any {
	return map[string]any{
		"id":               "run_abc",
		"pipeline_name":    "PR Review",
		"pipeline_slug":    "pr-review",
		"status":           "failed",
		"mode":             "agentless",
		"started_at":       "2026-06-10T12:00:00Z",
		"ended_at":         "2026-06-10T12:01:00Z",
		"duration_ms":      60000,
		"cost_usd":         0.1234,
		"triggered_via":    "schedule",
		"issue_identifier": "CRE-99",
		"error_message":    "step exploded",
		"failed_at_step":   "review",
		"current_step_id":  "review",
	}
}

func TestRoutineLogsRunE_SlugFreeTable(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipeline-runs/run_abc",
		clitest.JSONResponse(200, covPipelineRun()))

	out := covCaptureStdoutCli8(t, func() {
		if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_abc"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{
		"run_abc", "PR Review", "failed", "agentless", "60000ms", "$0.1234",
		"schedule", "CRE-99", "Error: step exploded", "Failed at step: review",
		"Current step: review", "--slug pr-review",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("slug-free output missing %q:\n%s", want, out)
		}
	}
}

func TestRoutineLogsRunE_SlugFreeJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipeline-runs/run_abc",
		clitest.JSONResponse(200, covPipelineRun()))
	covSetFlagCli8(t, routineLogsCmd, "json", "true")

	out := covCaptureStdoutCli8(t, func() {
		if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_abc"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, out)
	}
	if decoded["id"] != "run_abc" || decoded["status"] != "failed" {
		t.Errorf("json output wrong: %v", decoded)
	}
}

func TestRoutineLogsRunE_SlugFreeAPIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipeline-runs/ghost",
		clitest.ErrorResponse(404, "run not found"))

	err := routineLogsCmd.RunE(routineLogsCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Errorf("expected not-found; got %v", err)
	}
}

// covJournalRows returns journal entries in server (DESC) order for two
// runs; only run_target rows must survive the filter.
func covJournalRows() []map[string]any {
	return []map[string]any{
		{
			"id": "e4", "ts": "2026-06-10T12:00:03Z", "entry_type": "pipeline.run.completed",
			"severity": "info", "summary": "run done", "run_id": "run_target",
			"payload": map[string]any{"total_cost_usd": 0.05, "total_duration_ms": 2500.0},
		},
		{
			"id": "e3", "ts": "2026-06-10T12:00:02Z", "entry_type": "pipeline.step.completed",
			"severity": "info", "summary": "step ok", "run_id": "run_target",
			"payload": map[string]any{"cost_usd": 0.05, "duration_ms": 1500.0},
		},
		{
			"id": "e2", "ts": "2026-06-10T12:00:01Z", "entry_type": "pipeline.run.started",
			"summary": "run started", "run_id": "run_target",
		},
		{
			"id": "e1", "ts": "2026-06-10T11:00:00Z", "entry_type": "pipeline.run.completed",
			"severity": "info", "summary": "other run", "run_id": "run_other",
		},
	}
}

func TestRoutineLogsRunE_SlugTimelineTable(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/pr-review/runs",
		clitest.JSONResponse(200, covJournalRows()))
	covSetFlagCli8(t, routineLogsCmd, "slug", "pr-review")

	out := covCaptureStdoutCli8(t, func() {
		if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_target"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{
		"TIME", "run.started", "step.completed", "$0.0500", "1.50s",
		"Completed in 2.5s · cost $0.0500",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other run") {
		t.Errorf("entries from other runs must be filtered out:\n%s", out)
	}
	// Chronological: run.started line before step.completed line.
	if strings.Index(out, "run.started") > strings.Index(out, "step.completed") {
		t.Errorf("timeline not oldest-first:\n%s", out)
	}

	calls := stub.CallsFor("GET", "/api/v1/workspaces/"+covWSCli8+"/pipelines/pr-review/runs")
	if len(calls) != 1 {
		t.Fatalf("expected 1 runs GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "include_steps=1") || !strings.Contains(calls[0].Query, "limit=500") {
		t.Errorf("query params missing: %q", calls[0].Query)
	}
}

func TestRoutineLogsRunE_SlugTimelineFailedFooter(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	rows := []map[string]any{
		{
			"id": "f2", "ts": "2026-06-10T12:00:02Z", "entry_type": "pipeline.run.failed",
			"severity": "error", "summary": "run failed", "run_id": "run_target",
			"payload": map[string]any{"error_message": "gate unsatisfied"},
		},
		{
			"id": "f1", "ts": "2026-06-10T12:00:01Z", "entry_type": "pipeline.run.started",
			"summary": "run started", "run_id": "run_target",
		},
	}
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/pr-review/runs",
		clitest.JSONResponse(200, rows))
	covSetFlagCli8(t, routineLogsCmd, "slug", "pr-review")

	out := covCaptureStdoutCli8(t, func() {
		if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_target"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Failed: gate unsatisfied") {
		t.Errorf("missing failed footer:\n%s", out)
	}
}

func TestRoutineLogsRunE_SlugTimelineJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/pr-review/runs",
		clitest.JSONResponse(200, covJournalRows()))
	covSetFlagCli8(t, routineLogsCmd, "slug", "pr-review")
	covSetFlagCli8(t, routineLogsCmd, "json", "true")

	out := covCaptureStdoutCli8(t, func() {
		if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_target"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, out)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 matched entries, got %d", len(entries))
	}
	// Oldest-first after the reversal.
	if entries[0]["id"] != "e2" || entries[2]["id"] != "e4" {
		t.Errorf("entries not reversed to oldest-first: %v", entries)
	}
}

func TestRoutineLogsRunE_SlugNoMatches(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/pr-review/runs",
		clitest.JSONResponse(200, []map[string]any{}))
	covSetFlagCli8(t, routineLogsCmd, "slug", "pr-review")

	err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_nope"})
	if err == nil || !strings.Contains(err.Error(), `no entries found for run_id "run_nope"`) {
		t.Errorf("expected no-entries error; got %v", err)
	}
}

func TestRoutineLogsRunE_ErrorBranches(t *testing.T) {
	runPath := "/api/v1/workspaces/" + covWSCli8 + "/pipeline-runs/run_x"
	journalPath := "/api/v1/workspaces/" + covWSCli8 + "/pipelines/pr-review/runs"
	cases := []struct {
		name    string
		slug    bool
		route   func(*clitest.StubServer)
		noWS    bool
	}{
		{name: "no workspace", noWS: true},
		{name: "slug-free transport", route: func(s *clitest.StubServer) { s.OnGet(runPath, covAbort()) }},
		{name: "slug-free decode", route: func(s *clitest.StubServer) { s.OnGet(runPath, covNotJSON()) }},
		{name: "slug transport", slug: true, route: func(s *clitest.StubServer) { s.OnGet(journalPath, covAbort()) }},
		{name: "slug decode", slug: true, route: func(s *clitest.StubServer) { s.OnGet(journalPath, covNotJSON()) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			if c.noWS {
				cliCfg = &cli.CLIConfig{Token: "tok", Server: stub.URL()}
			}
			if c.slug {
				covSetFlagCli8(t, routineLogsCmd, "slug", "pr-review")
			}
			if c.route != nil {
				c.route(stub)
			}
			if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_x"}); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestFormatPayloadCostIntBranches(t *testing.T) {
	if got := formatPayloadCost(map[string]interface{}{"cost_usd": int(0)}); got != "—" {
		t.Errorf("int 0 cost: got %q", got)
	}
	if got := formatPayloadCost(map[string]interface{}{"cost_usd": int(2)}); got != "$2.0000" {
		t.Errorf("int cost: got %q", got)
	}
}

func TestRoutineLogsRunE_SlugAPIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipelines/pr-review/runs",
		clitest.ErrorResponse(500, "Internal server error"))
	covSetFlagCli8(t, routineLogsCmd, "slug", "pr-review")

	err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_x"})
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected API error; got %v", err)
	}
}
