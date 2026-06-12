package main

// Coverage for the validate RunE happy paths (which do NOT hit the
// os.Exit failure shortcut) plus collectStepTypes. Failure phases call
// printValidationError → os.Exit(1) and stay covered by the contract
// tests in cmd_routine_validate_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

const covValidDSL = `{
	"dsl_version": "1.0",
	"name": "demo-routine",
	"steps": [
		{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"},
		{"id":"b","type":"agent_run","agent_slug":"x","prompt":"again","needs":["a"]}
	]
}`

func TestRoutineValidateRunE_FileTableOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routine.json")
	if err := os.WriteFile(path, []byte(covValidDSL), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	covSetupCli10(t, "http://127.0.0.1:0") // no network needed; neutralises globals

	out, err := captureStdoutCovCli10(t, func() error {
		return routineValidateCmd.RunE(routineValidateCmd, []string{path})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "is a valid routine DSL") {
		t.Errorf("valid banner missing:\n%s", out)
	}
	if !strings.Contains(out, "demo-routine") || !strings.Contains(out, "Steps:     2") {
		t.Errorf("summary fields missing:\n%s", out)
	}
	if !strings.Contains(out, "agent_run") {
		t.Errorf("step types missing:\n%s", out)
	}
}

func TestRoutineValidateRunE_FileJSONOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routine.json")
	if err := os.WriteFile(path, []byte(covValidDSL), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	covSetupCli10(t, "http://127.0.0.1:0")
	setFlagCovCli10(t, routineValidateCmd, "json", "true")

	out, err := captureStdoutCovCli10(t, func() error {
		return routineValidateCmd.RunE(routineValidateCmd, []string{path})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{`"valid": true`, `"name": "demo-routine"`, `"step_count": 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("json missing %q:\n%s", want, out)
		}
	}
}

func TestRoutineValidateRunE_StdinInput(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	go func() {
		_, _ = w.WriteString(covValidDSL)
		_ = w.Close()
	}()

	out, runErr := captureStdoutCovCli10(t, func() error {
		return routineValidateCmd.RunE(routineValidateCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, "<stdin> is a valid routine DSL") {
		t.Errorf("stdin source label missing:\n%s", out)
	}
}

func TestRoutineValidateRunE_EmptyStdinErrors(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	_ = w.Close() // immediately EOF
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	runErr := routineValidateCmd.RunE(routineValidateCmd, nil)
	if runErr == nil || !strings.Contains(runErr.Error(), "empty input") {
		t.Errorf("expected empty-input error, got %v", runErr)
	}
}

func TestRoutineValidateRunE_MissingFileErrors(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	err := routineValidateCmd.RunE(routineValidateCmd, []string{filepath.Join(t.TempDir(), "nope.json")})
	if err == nil || !strings.Contains(err.Error(), "read file") {
		t.Errorf("expected read-file error, got %v", err)
	}
}

func TestRoutineValidateRunE_OutputsAndEgressRendered(t *testing.T) {
	withExtras := `{
		"dsl_version": "1.0",
		"name": "demo-routine",
		"steps": [{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}],
		"outputs": [{"name":"summary","type":"string"}],
		"egress_targets": ["https://example.com/webhook"]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "routine.json")
	if err := os.WriteFile(path, []byte(withExtras), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	covSetupCli10(t, "http://127.0.0.1:0")

	out, err := captureStdoutCovCli10(t, func() error {
		return routineValidateCmd.RunE(routineValidateCmd, []string{path})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Outputs:   1") {
		t.Errorf("outputs count missing:\n%s", out)
	}
	if !strings.Contains(out, "Egress:    https://example.com/webhook") {
		t.Errorf("egress targets missing:\n%s", out)
	}
}

func TestCollectStepTypes_DedupsPreservingOrder(t *testing.T) {
	dsl, err := pipeline.Parse([]byte(covValidDSL))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	got := collectStepTypes(dsl)
	if len(got) != 1 || got[0] != "agent_run" {
		t.Errorf("collectStepTypes = %v, want [agent_run]", got)
	}
}
