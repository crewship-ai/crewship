package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestEvalCmdStructure_IncludesGet(t *testing.T) {
	t.Parallel()
	have := map[string]bool{}
	for _, sub := range evalCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["get"] {
		t.Error("eval missing subcommand \"get\"")
	}
}

func TestEvalGetCmd_Args(t *testing.T) {
	t.Parallel()
	if err := evalGetCmd.Args(evalGetCmd, []string{}); err == nil {
		t.Error("get with no args should error")
	}
	if err := evalGetCmd.Args(evalGetCmd, []string{"a", "b"}); err == nil {
		t.Error("get with two args should error")
	}
	if err := evalGetCmd.Args(evalGetCmd, []string{"er_1"}); err != nil {
		t.Errorf("get with one arg should pass; got %v", err)
	}
}

func TestEvalGetRunE_ReplayHumanView(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs/er_1", clitest.JSONResponse(200, map[string]any{
		"id": "er_1", "kind": "replay", "mission_id": "MIS-42",
		"status": "completed", "result": "ok", "seed": 42, "signature": "sig-xyz",
		"total_tokens": 1200, "total_cost_usd": 0.15,
		"created_at": "2026-04-17T10:00:00Z", "completed_at": "2026-04-17T10:02:00Z",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalGetCmd.RunE(evalGetCmd, []string{"er_1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"er_1", "COMPLETED", "replay", "MIS-42", "Seed: 42", "sig-xyz", "Tokens: 1200", "$0.1500", "Result:", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	calls := s.CallsFor("GET", "/api/v1/eval/runs/er_1")
	if len(calls) != 1 {
		t.Fatalf("expected one GET, got %d", len(calls))
	}
}

func TestEvalGetRunE_RegressionShowsVerdict(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs/er_2", clitest.JSONResponse(200, map[string]any{
		"id": "er_2", "kind": "regression",
		"baseline_mission_id": "MIS-41", "candidate_mission_id": "MIS-42",
		"status": "completed", "result": "regressed: tool success -8%", "regressed": true,
		"created_at": "2026-04-17T10:15:00Z",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalGetCmd.RunE(evalGetCmd, []string{"er_2"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"MIS-41", "MIS-42", "Regressed: true", "regressed: tool success -8%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestEvalGetRunE_NoResultYet(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs/er_3", clitest.JSONResponse(200, map[string]any{
		"id": "er_3", "kind": "replay", "status": "running", "created_at": "2026-04-17T10:00:00Z",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalGetCmd.RunE(evalGetCmd, []string{"er_3"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "run is running") {
		t.Errorf("in-flight message missing:\n%s", out)
	}
}

func TestEvalGetRunE_FormatJSON(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs/er_4", clitest.JSONResponse(200, map[string]any{
		"id": "er_4", "kind": "replay", "status": "completed", "result": "ok",
	}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := evalGetCmd.RunE(evalGetCmd, []string{"er_4"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"result": "ok"`) {
		t.Errorf("json envelope missing result field:\n%s", out)
	}
}

func TestEvalGetRunE_NotFound(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs/er_missing", clitest.ErrorResponse(404, "eval run not found"))

	err := evalGetCmd.RunE(evalGetCmd, []string{"er_missing"})
	if err == nil || !strings.Contains(err.Error(), "eval run not found") {
		t.Errorf("expected 404 error; got %v", err)
	}
}

// TestEvalRunsRunE_JSONIncludesResultAndMetrics guards against the
// regression #1191 called out explicitly: `eval runs --format json`
// dropped result/signature/regressed/total_tokens/total_cost_usd even
// though the API already returned them (ListRuns selects the full
// RunRecord) — the CLI's local decode struct silently discarded the
// rest.
func TestEvalRunsRunE_JSONIncludesResultAndMetrics(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/eval/runs", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{
				"id": "er_5", "kind": "regression",
				"baseline_mission_id": "MIS-41", "candidate_mission_id": "MIS-42",
				"status": "completed", "result": "regressed: cost +22%", "regressed": true,
				"total_tokens": 900, "total_cost_usd": 0.33, "signature": "sig-9",
				"created_at": "2026-06-10T00:00:00Z",
			},
		},
		"count": 1,
	}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := evalRunsCmd.RunE(evalRunsCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{
		`"result": "regressed: cost +22%"`,
		`"regressed": true`,
		`"total_tokens": 900`,
		`"total_cost_usd": 0.33`,
		`"signature": "sig-9"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("eval runs json missing %q:\n%s", want, out)
		}
	}
}

func TestEvalGetRunE_AuthGates(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{}
	if err := evalGetCmd.RunE(evalGetCmd, []string{"er_1"}); err == nil {
		t.Error("expected not-logged-in error")
	}
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := evalGetCmd.RunE(evalGetCmd, []string{"er_1"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}
