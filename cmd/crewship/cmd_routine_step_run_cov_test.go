package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covStepRunPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipelines/parse-invoice/step_run"

func TestParseInputFixture(t *testing.T) {
	// Inline JSON object.
	m, err := parseInputFixture(`{"name":"a.pdf","n":3}`)
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	if m["name"] != "a.pdf" {
		t.Errorf("inline parse wrong: %+v", m)
	}
	// Empty is allowed (nil map, no error).
	if m2, err := parseInputFixture("  "); err != nil || m2 != nil {
		t.Errorf("empty spec: got %+v, %v", m2, err)
	}
	// @file.
	dir := t.TempDir()
	fp := filepath.Join(dir, "fx.json")
	if err := os.WriteFile(fp, []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if m3, err := parseInputFixture("@" + fp); err != nil || m3["k"] != "v" {
		t.Errorf("@file: got %+v, %v", m3, err)
	}
	// Missing file → error.
	if _, err := parseInputFixture("@/no/such/file.json"); err == nil {
		t.Error("expected error for missing fixture file")
	}
	// Non-object JSON → error.
	if _, err := parseInputFixture(`[1,2,3]`); err == nil {
		t.Error("expected error for non-object fixture")
	}
	// Garbage → error.
	if _, err := parseInputFixture(`{not json`); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseOutputsFixture(t *testing.T) {
	// Inline object: string value verbatim, object value stringified to JSON.
	m, err := parseOutputsFixture(`{"parse":"raw text","verify":{"ok":true}}`)
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	if m["parse"] != "raw text" {
		t.Errorf("string value not verbatim: %q", m["parse"])
	}
	if m["verify"] != `{"ok":true}` {
		t.Errorf("object value not stringified: %q", m["verify"])
	}
	// Empty → nil, no error.
	if m2, err := parseOutputsFixture(""); err != nil || m2 != nil {
		t.Errorf("empty: got %+v, %v", m2, err)
	}
	// @file.
	dir := t.TempDir()
	fp := filepath.Join(dir, "out.json")
	if err := os.WriteFile(fp, []byte(`{"parse":"from file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if m3, err := parseOutputsFixture("@" + fp); err != nil || m3["parse"] != "from file" {
		t.Errorf("@file: got %+v, %v", m3, err)
	}
	// Non-object JSON → error.
	if _, err := parseOutputsFixture(`["a","b"]`); err == nil {
		t.Error("expected error for non-object outputs fixture")
	}
	// Missing file → error.
	if _, err := parseOutputsFixture("@/no/such/outputs.json"); err == nil {
		t.Error("expected error for missing outputs file")
	}
}

func TestRoutineStepRunRunE_PrintsWarnings(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covStepRunPath, clitest.JSONResponse(200, cli.StepRunResult{
		StepID: "reconcile", StepType: "agent_run", Adapter: "claude_code", Model: "m",
		Output: "ok", Valid: true, Simulated: true,
		Warnings: []string{`prompt references {{ steps.parse.output }} but no --outputs fixture was provided for "parse"`},
	}))
	covSetupCli10(t, s.URL())
	defer func() { stepRunInput = ""; stepRunOutputs = ""; stepRunTierOverride = "" }()

	out, err := captureStdoutCovCli10(t, func() error {
		return routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "reconcile"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "⚠") || !strings.Contains(out, "steps.parse.output") {
		t.Errorf("warning not surfaced in human output:\n%s", out)
	}
}

func TestRoutineStepRunRunE_PrintsVerdictAndOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covStepRunPath, clitest.JSONResponse(200, cli.StepRunResult{
		StepID: "extract", StepType: "agent_run", Adapter: "claude_code", Model: "claude-haiku-4-5",
		Output: `{"total":42}`, Valid: true, CostUSD: 0.0021, TokensIn: 120, TokensOut: 40,
		DurationMs: 4210, Simulated: true,
	}))
	covSetupCli10(t, s.URL())
	stepRunInput = `{"name":"sample.pdf"}`
	stepRunTierOverride = "fast"
	defer func() { stepRunInput = ""; stepRunTierOverride = "" }()

	out, err := captureStdoutCovCli10(t, func() error {
		return routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "extract"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "claude-haiku-4-5") {
		t.Errorf("verdict/model line missing:\n%s", out)
	}
	if !strings.Contains(out, "simulated (no run record)") {
		t.Errorf("simulation marker missing:\n%s", out)
	}
	if !strings.Contains(out, "\"total\": 42") {
		t.Errorf("pretty output missing:\n%s", out)
	}
}

func TestRoutineStepRunRunE_FailVerdictShowsReason(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covStepRunPath, clitest.JSONResponse(200, cli.StepRunResult{
		StepID: "extract", StepType: "agent_run", Adapter: "claude_code", Model: "m",
		Output: "not json", Valid: false, ValidationReason: "output is not valid JSON", Simulated: true,
	}))
	covSetupCli10(t, s.URL())
	defer func() { stepRunInput = ""; stepRunTierOverride = "" }()

	out, err := captureStdoutCovCli10(t, func() error {
		return routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "extract"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "output is not valid JSON") {
		t.Errorf("fail verdict / reason missing:\n%s", out)
	}
}

func TestRoutineStepRunRunE_FormatJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covStepRunPath, clitest.JSONResponse(200, cli.StepRunResult{
		StepID: "extract", StepType: "agent_run", Output: "done", Valid: true, Simulated: true,
	}))
	covSetupCli10(t, s.URL())
	defer func() { stepRunInput = ""; stepRunTierOverride = ""; flagFormat = "" }()
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "extract"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"output": "done"`) || !strings.Contains(out, `"simulated": true`) {
		t.Errorf("json envelope missing fields:\n%s", out)
	}
}

func TestRoutineStepRunRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covStepRunPath, clitest.ErrorResponse(404, "step not found: ghost"))
	covSetupCli10(t, s.URL())
	defer func() { stepRunInput = ""; stepRunTierOverride = "" }()
	if err := routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "ghost"}); err == nil {
		t.Error("expected error from 404")
	}
}

func TestRoutineStepRunRunE_BadFixture(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	stepRunInput = `{broken`
	defer func() { stepRunInput = "" }()
	if err := routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "extract"}); err == nil {
		t.Error("expected error from malformed --input")
	}
}

func TestRoutineStepRunRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	defer func() { stepRunInput = "" }()
	if err := routineStepRunCmd.RunE(routineStepRunCmd, []string{"parse-invoice", "extract"}); err == nil ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}
