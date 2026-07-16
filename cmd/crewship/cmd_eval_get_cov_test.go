package main

import (
	"encoding/json"
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

// TestEvalReplayRunE_FormatJSON guards #1221: `eval replay --format json`
// used to always print the human "Replay queued: run_id=... status=..."
// line via a bare fmt.Printf, ignoring --format entirely.
func TestEvalReplayRunE_FormatJSON(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/replay", clitest.JSONResponse(200, map[string]any{
		"run_id": "er_replay_1", "status": "queued",
	}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", err, out)
	}
	if payload["run_id"] != "er_replay_1" || payload["status"] != "queued" {
		t.Errorf("payload = %v", payload)
	}
	if strings.Contains(out, "Replay queued:") {
		t.Errorf("--format json must not fall back to the human confirmation line; got:\n%s", out)
	}
}

// TestEvalReplayRunE_DefaultStaysHuman confirms the human queue-confirmation
// text is unchanged when no --format is given.
func TestEvalReplayRunE_DefaultStaysHuman(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/replay", clitest.JSONResponse(200, map[string]any{
		"run_id": "er_replay_2", "status": "queued",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Replay queued: run_id=er_replay_2 status=queued") {
		t.Errorf("human output changed; got:\n%s", out)
	}
}

// TestEvalReplayRunE_FormatYAMLFieldNamesMatchJSON guards against the
// #1211-class bug: --format yaml must use the same snake_case field names
// as --format json, not yaml.v3's lowercased-fieldname fallback.
func TestEvalReplayRunE_FormatYAMLFieldNamesMatchJSON(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/replay", clitest.JSONResponse(200, map[string]any{
		"run_id": "er_replay_3", "status": "queued",
	}))
	flagFormat = "yaml"

	out := covCaptureStdoutCli9(t, func() {
		if err := evalReplayCmd.RunE(evalReplayCmd, []string{"MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "run_id: er_replay_3") {
		t.Errorf("--format yaml must use snake_case run_id, not runid; got:\n%s", out)
	}
}

// TestEvalRegressionRunE_FormatJSON guards #1221 for `eval regression`,
// which had the same fmt.Printf-only bug as `eval replay`.
func TestEvalRegressionRunE_FormatJSON(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/regression", clitest.JSONResponse(200, map[string]any{
		"run_id": "er_regr_1", "status": "queued",
	}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"MIS-41", "MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", err, out)
	}
	if payload["run_id"] != "er_regr_1" || payload["status"] != "queued" {
		t.Errorf("payload = %v", payload)
	}
	if strings.Contains(out, "Regression queued:") {
		t.Errorf("--format json must not fall back to the human confirmation line; got:\n%s", out)
	}
}

// TestEvalRegressionRunE_DefaultStaysHuman confirms the human queue-
// confirmation text is unchanged when no --format is given.
func TestEvalRegressionRunE_DefaultStaysHuman(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/eval/regression", clitest.JSONResponse(200, map[string]any{
		"run_id": "er_regr_2", "status": "queued",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"MIS-41", "MIS-42"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Regression queued: run_id=er_regr_2 status=queued") {
		t.Errorf("human output changed; got:\n%s", out)
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
