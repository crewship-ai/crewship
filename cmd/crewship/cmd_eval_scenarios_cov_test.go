package main

// Coverage tests for cmd_eval_scenarios.go beyond the aggregation
// helpers already covered in cmd_eval_scenarios_test.go: the RunE batch
// loop, slug resolution, single-run execution, and the report renderers.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covSetFormat pins the global --format resolution for the test.
func covSetFormat(t *testing.T, format string) {
	t.Helper()
	old := flagFormat
	flagFormat = format
	t.Cleanup(func() { flagFormat = old })
}

// covEvalRunPath is the run endpoint for a scenario slug under the
// canonical test workspace.
func covEvalRunPath(slug string) string {
	return "/api/v1/workspaces/" + covWorkspaceIDCli6 + "/pipelines/" + slug + "/run"
}

func covCompletedRun(slug string) clitest.Handler {
	return clitest.JSONResponse(200, map[string]any{
		"run_id": "run-" + slug, "status": "COMPLETED",
		"duration_ms": 120, "cost_usd": 0.0042,
	})
}

// ─── runEvalScenarios (RunE) ─────────────────────────────────────────────

func TestRunEvalScenarios_RunsValidation(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, evalScenariosCmd, "runs", "0")

	err := runEvalScenarios(evalScenariosCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--runs must be >= 1") {
		t.Errorf("expected runs validation, got %v", err)
	}
}

func TestRunEvalScenarios_BadInputsJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, evalScenariosCmd, "scenarios", "eval-a")
	covSetFlagCli6(t, evalScenariosCmd, "inputs", "{not json")

	err := runEvalScenarios(evalScenariosCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "parse --inputs JSON") {
		t.Errorf("expected inputs parse error, got %v", err)
	}
}

func TestRunEvalScenarios_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := runEvalScenarios(evalScenariosCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

func TestRunEvalScenarios_SweepHappy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, evalScenariosCmd, "scenarios", "eval-a,eval-b")
	covSetFlagCli6(t, evalScenariosCmd, "tiers", "fast")
	covSetFlagCli6(t, evalScenariosCmd, "runs", "2")
	covSetFlagCli6(t, evalScenariosCmd, "inputs", `{"text":"hello"}`)

	stub.OnPost(covEvalRunPath("eval-a"), covCompletedRun("eval-a"))
	stub.OnPost(covEvalRunPath("eval-b"), covCompletedRun("eval-b"))

	// Progress lines go to cmd.OutOrStderr(), which resolves to the
	// out-writer when one is set.
	errBuf := &bytes.Buffer{}
	evalScenariosCmd.SetOut(errBuf)
	t.Cleanup(func() { evalScenariosCmd.SetOut(nil) })

	out, err := covCaptureStdoutCli6(t, func() error {
		return runEvalScenarios(evalScenariosCmd, nil)
	})
	if err != nil {
		t.Fatalf("runEvalScenarios: %v", err)
	}

	// 2 scenarios × 1 tier × 2 runs = 4 invocations.
	for _, slug := range []string{"eval-a", "eval-b"} {
		calls := stub.CallsFor("POST", covEvalRunPath(slug))
		if len(calls) != 2 {
			t.Fatalf("%s: %d run calls, want 2", slug, len(calls))
		}
		body := covDecodeBody(t, calls[0].Body)
		if body["tier_override"] != "fast" {
			t.Errorf("%s: tier_override = %v", slug, body["tier_override"])
		}
		inputs, _ := body["inputs"].(map[string]any)
		if inputs["text"] != "hello" {
			t.Errorf("%s: inputs not forwarded: %v", slug, body["inputs"])
		}
	}

	progress := errBuf.String()
	if !strings.Contains(progress, "Running 4 invocations: 2 scenarios × 1 tiers × 2 runs") {
		t.Errorf("progress header wrong: %q", progress)
	}
	if !strings.Contains(progress, "eval-a") || !strings.Contains(progress, "COMPLETED") {
		t.Errorf("per-run lines missing: %q", progress)
	}
	// Default table report has scenario rows with pass/total cells.
	if !strings.Contains(out, "eval-a") || !strings.Contains(out, "2/2") {
		t.Errorf("table report wrong: %q", out)
	}
}

func TestRunEvalScenarios_ListsWorkspaceRoutines(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, evalScenariosCmd, "scenarios", "tiers", "runs", "inputs", "fail-fast")

	stub.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli6+"/pipelines", clitest.JSONResponse(200, []map[string]string{
		{"slug": "eval-z"}, {"slug": "ops-routine"},
	}))
	stub.OnPost(covEvalRunPath("eval-z"), covCompletedRun("eval-z"))

	errBuf := &bytes.Buffer{}
	evalScenariosCmd.SetOut(errBuf)
	t.Cleanup(func() { evalScenariosCmd.SetOut(nil) })

	if _, err := covCaptureStdoutCli6(t, func() error {
		return runEvalScenarios(evalScenariosCmd, nil)
	}); err != nil {
		t.Fatalf("runEvalScenarios: %v", err)
	}
	if n := len(stub.CallsFor("POST", covEvalRunPath("eval-z"))); n != 1 {
		t.Errorf("eval-z run calls = %d, want 1", n)
	}
	if n := len(stub.CallsFor("POST", covEvalRunPath("ops-routine"))); n != 0 {
		t.Errorf("non-eval routine must not be run, got %d calls", n)
	}
}

func TestRunEvalScenarios_NoScenariosResolved(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, evalScenariosCmd, "scenarios", "tiers", "runs", "inputs", "fail-fast")

	stub.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli6+"/pipelines", clitest.JSONResponse(200, []map[string]string{
		{"slug": "ops-only"},
	}))

	err := runEvalScenarios(evalScenariosCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no scenarios resolved") {
		t.Errorf("expected no-scenarios error, got %v", err)
	}
}

func TestRunEvalScenarios_FailFastAborts(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, evalScenariosCmd, "scenarios", "eval-a")
	covSetFlagCli6(t, evalScenariosCmd, "runs", "3")
	covSetFlagCli6(t, evalScenariosCmd, "fail-fast", "true")

	stub.OnPost(covEvalRunPath("eval-a"), clitest.ErrorResponse(500, "step exploded"))

	errBuf := &bytes.Buffer{}
	evalScenariosCmd.SetOut(errBuf)
	t.Cleanup(func() { evalScenariosCmd.SetOut(nil) })

	if _, err := covCaptureStdoutCli6(t, func() error {
		return runEvalScenarios(evalScenariosCmd, nil)
	}); err != nil {
		t.Fatalf("fail-fast still renders the report and returns nil, got %v", err)
	}
	if n := len(stub.CallsFor("POST", covEvalRunPath("eval-a"))); n != 1 {
		t.Errorf("fail-fast must stop after the first failure, got %d calls", n)
	}
	if !strings.Contains(errBuf.String(), "fail-fast: aborting after first failure") {
		t.Errorf("fail-fast banner missing: %q", errBuf.String())
	}
}

// ─── resolveScenarioSlugs ────────────────────────────────────────────────

func TestResolveScenarioSlugs_SuppliedSorted(t *testing.T) {
	got, err := resolveScenarioSlugs(nil, covWorkspaceIDCli6, "eval-b, eval-a")
	if err != nil {
		t.Fatalf("resolveScenarioSlugs: %v", err)
	}
	if len(got) != 2 || got[0] != "eval-a" || got[1] != "eval-b" {
		t.Errorf("got %v, want sorted [eval-a eval-b]", got)
	}
}

func TestResolveScenarioSlugs_FiltersAndSorts(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli6+"/pipelines", clitest.JSONResponse(200, []map[string]string{
		{"slug": "eval-zulu"}, {"slug": "daily-report"}, {"slug": "eval-alpha"},
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	got, err := resolveScenarioSlugs(client, covWorkspaceIDCli6, "")
	if err != nil {
		t.Fatalf("resolveScenarioSlugs: %v", err)
	}
	if len(got) != 2 || got[0] != "eval-alpha" || got[1] != "eval-zulu" {
		t.Errorf("got %v, want [eval-alpha eval-zulu]", got)
	}
}

func TestResolveScenarioSlugs_HTTPError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli6+"/pipelines", clitest.ErrorResponse(500, "boom"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := resolveScenarioSlugs(client, covWorkspaceIDCli6, "")
	if err == nil || !strings.Contains(err.Error(), "list routines: HTTP 500") {
		t.Errorf("expected HTTP error, got %v", err)
	}
}

func TestResolveScenarioSlugs_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli6+"/pipelines", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := resolveScenarioSlugs(client, covWorkspaceIDCli6, "")
	if err == nil || !strings.Contains(err.Error(), "decode routine list") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestResolveScenarioSlugs_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close()

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := resolveScenarioSlugs(client, covWorkspaceIDCli6, "")
	if err == nil || !strings.Contains(err.Error(), "list routines") {
		t.Errorf("expected transport error, got %v", err)
	}
}

// ─── executeOneScenario ──────────────────────────────────────────────────

func TestExecuteOneScenario_Success(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(covEvalRunPath("eval-a"), clitest.JSONResponse(200, map[string]any{
		"run_id": "r1", "status": "COMPLETED", "duration_ms": 200, "cost_usd": 0.01,
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out := executeOneScenario(client, covWorkspaceIDCli6, "eval-a", "fast", 1, map[string]any{"k": "v"})
	if out.Status != "COMPLETED" || out.RunID != "r1" || out.DurationMs != 200 || out.CostUSD != 0.01 {
		t.Errorf("outcome wrong: %+v", out)
	}
	body := covDecodeBody(t, stub.CallsFor("POST", covEvalRunPath("eval-a"))[0].Body)
	if body["tier_override"] != "fast" {
		t.Errorf("tier_override missing: %v", body)
	}
}

func TestExecuteOneScenario_NoTierOmitsOverride(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(covEvalRunPath("eval-a"), covCompletedRun("eval-a"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out := executeOneScenario(client, covWorkspaceIDCli6, "eval-a", "", 1, nil)
	if out.Status != "COMPLETED" {
		t.Fatalf("outcome wrong: %+v", out)
	}
	body := covDecodeBody(t, stub.CallsFor("POST", covEvalRunPath("eval-a"))[0].Body)
	if _, ok := body["tier_override"]; ok {
		t.Errorf("tier_override must be absent for authored tier: %v", body)
	}
	if _, ok := body["inputs"]; !ok {
		t.Errorf("inputs must default to {}: %v", body)
	}
}

func TestExecuteOneScenario_HTTPError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(covEvalRunPath("eval-a"), clitest.ErrorResponse(503, "queue full"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out := executeOneScenario(client, covWorkspaceIDCli6, "eval-a", "", 2, nil)
	if out.Status != "HTTP_503" {
		t.Errorf("Status = %q, want HTTP_503", out.Status)
	}
	if !strings.Contains(out.ErrorMessage, "queue full") {
		t.Errorf("ErrorMessage = %q", out.ErrorMessage)
	}
	if out.Attempt != 2 {
		t.Errorf("Attempt = %d", out.Attempt)
	}
}

func TestExecuteOneScenario_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost(covEvalRunPath("eval-a"), clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out := executeOneScenario(client, covWorkspaceIDCli6, "eval-a", "", 1, nil)
	if out.Status != "DECODE_ERROR" {
		t.Errorf("Status = %q, want DECODE_ERROR", out.Status)
	}
}

func TestExecuteOneScenario_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close()

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	out := executeOneScenario(client, covWorkspaceIDCli6, "eval-a", "", 1, nil)
	if out.Status != "ERROR" || out.ErrorMessage == "" {
		t.Errorf("outcome wrong: %+v", out)
	}
}

// ─── printOutcomeLine / renderEvalReport ─────────────────────────────────

func TestPrintOutcomeLine_Completed(t *testing.T) {
	buf := &bytes.Buffer{}
	printOutcomeLine(buf, scenarioOutcome{
		Scenario: "eval-a", Tier: "", Attempt: 1, Status: "COMPLETED",
		DurationMs: 150, CostUSD: 0.002,
	})
	out := buf.String()
	if !strings.Contains(out, "eval-a") || !strings.Contains(out, "(authored)") || !strings.Contains(out, "COMPLETED") {
		t.Errorf("completed line wrong: %q", out)
	}
	if strings.Contains(out, "fail @") {
		t.Errorf("no failure detail expected: %q", out)
	}
}

func TestPrintOutcomeLine_FailureDetail(t *testing.T) {
	buf := &bytes.Buffer{}
	printOutcomeLine(buf, scenarioOutcome{
		Scenario: "eval-a", Tier: "fast", Attempt: 2, Status: "FAILED",
		FailedAtStep: "gate", ErrorMessage: "rubric mismatch",
	})
	out := buf.String()
	if !strings.Contains(out, "fail @ gate: rubric mismatch") {
		t.Errorf("failure detail missing: %q", out)
	}
}

func covSampleOutcomes() ([]scenarioOutcome, []string, []string) {
	outcomes := []scenarioOutcome{
		{Scenario: "eval-a", Tier: "fast", Attempt: 1, Status: "COMPLETED", DurationMs: 100, CostUSD: 0.01},
		{Scenario: "eval-a", Tier: "fast", Attempt: 2, Status: "FAILED", DurationMs: 200, CostUSD: 0.03},
	}
	return outcomes, []string{"eval-a"}, []string{"fast"}
}

func TestRenderEvalReport_JSON(t *testing.T) {
	saveCLIState(t)
	covSetFormat(t, "json")
	outcomes, scenarios, tiers := covSampleOutcomes()

	out, err := covCaptureStdoutCli6(t, func() error {
		return renderEvalReport(evalScenariosCmd, outcomes, scenarios, tiers)
	})
	if err != nil {
		t.Fatalf("renderEvalReport: %v", err)
	}
	for _, want := range []string{`"matrix"`, `"outcomes"`, `"generated"`, `"eval-a"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json report missing %s: %q", want, out)
		}
	}
}

func TestRenderEvalReport_YAML(t *testing.T) {
	saveCLIState(t)
	covSetFormat(t, "yaml")
	outcomes, scenarios, tiers := covSampleOutcomes()

	out, err := covCaptureStdoutCli6(t, func() error {
		return renderEvalReport(evalScenariosCmd, outcomes, scenarios, tiers)
	})
	if err != nil {
		t.Fatalf("renderEvalReport: %v", err)
	}
	if !strings.Contains(out, "matrix:") || !strings.Contains(out, "eval-a") {
		t.Errorf("yaml report wrong: %q", out)
	}
}

func TestRenderEvalReport_Markdown(t *testing.T) {
	saveCLIState(t)
	covSetFormat(t, "markdown")
	outcomes, scenarios, tiers := covSampleOutcomes()

	buf := &bytes.Buffer{}
	evalScenariosCmd.SetOut(buf)
	t.Cleanup(func() { evalScenariosCmd.SetOut(nil) })

	if err := renderEvalReport(evalScenariosCmd, outcomes, scenarios, tiers); err != nil {
		t.Fatalf("renderEvalReport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "# Eval scenarios — cross-tier matrix") {
		t.Errorf("markdown header missing: %q", out)
	}
	if !strings.Contains(out, "| `eval-a` |") || !strings.Contains(out, "1/2") {
		t.Errorf("markdown cells wrong: %q", out)
	}
}

func TestRenderEvalReport_TableDefault(t *testing.T) {
	saveCLIState(t)
	covSetFormat(t, "")
	cliCfg = &cli.CLIConfig{} // no config-level format override
	outcomes, scenarios, tiers := covSampleOutcomes()

	out, err := covCaptureStdoutCli6(t, func() error {
		return renderEvalReport(evalScenariosCmd, outcomes, scenarios, tiers)
	})
	if err != nil {
		t.Fatalf("renderEvalReport: %v", err)
	}
	if !strings.Contains(out, "eval-a") || !strings.Contains(out, "1/2") {
		t.Errorf("table report wrong: %q", out)
	}
}

// ─── tiny helpers ────────────────────────────────────────────────────────

func TestTruncEvalLine(t *testing.T) {
	if got := truncEvalLine("short", 10); got != "short" {
		t.Errorf("short passthrough wrong: %q", got)
	}
	if got := truncEvalLine("0123456789abc", 10); got != "0123456789..." {
		t.Errorf("truncation wrong: %q", got)
	}
}

func TestReadBodyBytes(t *testing.T) {
	got := readBodyBytes(strings.NewReader("hello"))
	if string(got) != "hello" {
		t.Errorf("readBodyBytes = %q", got)
	}
	// Larger than the 8KiB cap: a single Read returns at most cap bytes.
	big := strings.Repeat("x", 20*1024)
	got = readBodyBytes(strings.NewReader(big))
	if len(got) > 8*1024 {
		t.Errorf("readBodyBytes returned %d bytes, cap is 8KiB", len(got))
	}
}

func TestAsWriter(t *testing.T) {
	buf := &bytes.Buffer{}
	w := asWriter(buf)
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "ok" {
		t.Errorf("asWriter must pass through, got %q", buf.String())
	}
}
