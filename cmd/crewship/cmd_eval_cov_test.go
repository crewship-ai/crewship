package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestEvalReplayRunE_WithSeed(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/replay", clitest.JSONResponse(202, map[string]string{
		"run_id": "ev_1", "status": "queued",
	}))
	covSetFlagCli9(t, evalReplayCmd, "seed", "42")

	out := covCaptureStdoutCli9(t, func() {
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Replay queued: run_id=ev_1 status=queued") {
		t.Errorf("replay confirmation missing:\n%s", out)
	}

	calls := s.CallsFor("POST", "/api/v1/eval/replay")
	if len(calls) != 1 {
		t.Fatalf("expected one replay POST, got %d", len(calls))
	}
	var body map[string]any
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["mission_id"] != "MIS-42" || body["seed"] != 42.0 {
		t.Errorf("replay body = %v", body)
	}
}

func TestEvalReplayRunE_ZeroSeedOmitted(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/replay", clitest.JSONResponse(202, map[string]string{"run_id": "ev_2", "status": "queued"}))

	_ = covCaptureStdoutCli9(t, func() {
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"MIS-7"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	calls := s.CallsFor("POST", "/api/v1/eval/replay")
	if len(calls) != 1 {
		t.Fatalf("expected one POST, got %d", len(calls))
	}
	if strings.Contains(string(calls[0].Body), "seed") {
		t.Errorf("seed=0 must be omitted from body: %s", calls[0].Body)
	}
}

func TestEvalRegressionRunE(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/regression", clitest.JSONResponse(202, map[string]string{
		"run_id": "ev_3", "status": "queued",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"MIS-41", "MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Regression queued: run_id=ev_3") {
		t.Errorf("regression confirmation missing:\n%s", out)
	}

	calls := s.CallsFor("POST", "/api/v1/eval/regression")
	if len(calls) != 1 {
		t.Fatalf("expected one POST, got %d", len(calls))
	}
	var body map[string]string
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["baseline_mission_id"] != "MIS-41" || body["candidate_mission_id"] != "MIS-42" {
		t.Errorf("regression body = %v", body)
	}
}

func TestEvalRunsRunE_Table(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]string{
			{"id": "ev_1", "kind": "replay", "mission_id": "MIS-42", "status": "completed", "created_at": "2026-06-10"},
		},
		"count": 1,
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalRunsCmd.RunE(evalRunsCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"ev_1", "replay", "MIS-42", "completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("runs table missing %q:\n%s", want, out)
		}
	}
	calls := s.CallsFor("GET", "/api/v1/eval/runs")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=50") {
		t.Errorf("default limit not forwarded: %+v", calls)
	}
}

func TestEvalRunsRunE_EmptyAndJSON(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/eval/runs", clitest.JSONResponse(200, map[string]any{"rows": []map[string]string{}, "count": 0}))
		out := covCaptureStdoutCli9(t, func() {
			if err := evalRunsCmd.RunE(evalRunsCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "(no eval runs recorded yet)") {
			t.Errorf("empty-state line missing:\n%s", out)
		}
	})
	t.Run("json", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/eval/runs", clitest.JSONResponse(200, map[string]any{
			"rows": []map[string]string{{"id": "ev_9", "kind": "regression"}},
		}))
		flagFormat = "json"
		out := covCaptureStdoutCli9(t, func() {
			if err := evalRunsCmd.RunE(evalRunsCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"ev_9"`) {
			t.Errorf("json output missing row:\n%s", out)
		}
	})
}

func TestEvalCmds_ErrorPaths(t *testing.T) {
	t.Run("replay server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/eval/replay", clitest.ErrorResponse(404, "mission missing"))
		err := evalReplayCmd.RunE(evalReplayCmd, []string{"MIS-0"})
		if err == nil || !strings.Contains(err.Error(), "mission missing") {
			t.Errorf("expected 404 error; got %v", err)
		}
	})
	t.Run("regression server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/eval/regression", clitest.ErrorResponse(500, "diff broke"))
		err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"a", "b"})
		if err == nil || !strings.Contains(err.Error(), "diff broke") {
			t.Errorf("expected 500 error; got %v", err)
		}
	})
	t.Run("runs server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/eval/runs", clitest.ErrorResponse(500, "list broke"))
		err := evalRunsCmd.RunE(evalRunsCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "list broke") {
			t.Errorf("expected 500 error; got %v", err)
		}
	})
	t.Run("auth gates", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"m"}); err == nil {
			t.Error("replay: expected not-logged-in")
		}
		if err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"a", "b"}); err == nil {
			t.Error("regression: expected not-logged-in")
		}
		if err := evalRunsCmd.RunE(evalRunsCmd, nil); err == nil {
			t.Error("runs: expected not-logged-in")
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"m"}); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("replay: expected workspace error; got %v", err)
		}
		if err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("regression: expected workspace error; got %v", err)
		}
		if err := evalRunsCmd.RunE(evalRunsCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("runs: expected workspace error; got %v", err)
		}
	})
}
