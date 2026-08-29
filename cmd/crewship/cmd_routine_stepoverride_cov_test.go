package main

// Coverage for `routine step-override set/clear` (cmd_routine_overrides.go).
// routineStepOverrideListCmd's -f json contract is already covered in
// cmd_json_contract_test.go; this file closes the rest of the group —
// including the parent routineStepOverrideCmd, whose name appears nowhere
// else in the suite.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineStepOverrideCmd_HasChildren(t *testing.T) {
	have := map[string]bool{}
	for _, c := range routineStepOverrideCmd.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"set", "clear", "list"} {
		if !have[want] {
			t.Errorf("routineStepOverrideCmd missing subcommand %q", want)
		}
	}
}

func TestRoutineStepOverrideSetRunE_RequiresPromptOrModel(t *testing.T) {
	covSetupCli5(t)
	err := routineStepOverrideSetCmd.RunE(routineStepOverrideSetCmd, []string{"my-routine", "step_1"})
	if err == nil || !strings.Contains(err.Error(), "pass --prompt and/or --model") {
		t.Errorf("want prompt/model-required error, got %v", err)
	}
}

func TestRoutineStepOverrideSetRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/workspaces/" + covWSCli5 + "/pipelines/my-routine/steps/step_1/override"
	stub.OnPut(path, clitest.JSONResponse(200, map[string]any{"ok": true}))
	if err := routineStepOverrideSetCmd.Flags().Set("prompt", "Be terser."); err != nil {
		t.Fatal(err)
	}
	if err := routineStepOverrideSetCmd.Flags().Set("model", "smart"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = routineStepOverrideSetCmd.Flags().Set("prompt", "")
		_ = routineStepOverrideSetCmd.Flags().Set("model", "")
	})

	var runErr error
	out := covCaptureStdoutCli5(t, func() {
		runErr = routineStepOverrideSetCmd.RunE(routineStepOverrideSetCmd, []string{"my-routine", "step_1"})
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, `Override set for step "step_1" of "my-routine"`) {
		t.Errorf("output = %q", out)
	}
	calls := stub.CallsFor("PUT", path)
	if len(calls) != 1 {
		t.Fatalf("want 1 PUT call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["prompt"] != "Be terser." || body["model_override"] != "smart" {
		t.Errorf("body = %v", body)
	}
}

func TestRoutineStepOverrideClearRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/workspaces/" + covWSCli5 + "/pipelines/my-routine/steps/step_1/override"
	stub.OnDelete(path, clitest.EmptyResponse(http.StatusNoContent))

	var runErr error
	out := covCaptureStdoutCli5(t, func() {
		runErr = routineStepOverrideClearCmd.RunE(routineStepOverrideClearCmd, []string{"my-routine", "step_1"})
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, `Override cleared for step "step_1" of "my-routine"`) {
		t.Errorf("output = %q", out)
	}
	if n := len(stub.CallsFor("DELETE", path)); n != 1 {
		t.Errorf("DELETE calls = %d, want 1", n)
	}
}

func TestRoutineStepOverrideClearRunE_NotFound(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/workspaces/" + covWSCli5 + "/pipelines/ghost/steps/step_1/override"
	stub.OnDelete(path, clitest.ErrorResponse(http.StatusNotFound, "pipeline not found"))

	err := routineStepOverrideClearCmd.RunE(routineStepOverrideClearCmd, []string{"ghost", "step_1"})
	if err == nil || !strings.Contains(err.Error(), "pipeline not found") {
		t.Errorf("want not-found error surfaced, got %v", err)
	}
}

// List's happy (non-empty) path — the empty-array contract is already
// covered by TestRoutineStepOverrideList_JSONContract.
func TestRoutineStepOverrideListRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/overrides",
		clitest.JSONResponse(200, map[string]any{
			"overrides": []map[string]any{
				{"step_id": "step_1", "prompt": "Be terser.", "model_override": "smart"},
			},
		}))
	out := humanOut(t, func() error {
		return routineStepOverrideListCmd.RunE(routineStepOverrideListCmd, []string{"my-routine"})
	})
	for _, want := range []string{"step_1", "smart", "Be terser."} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}
