package main

// Coverage tests for cmd_routine_bench.go — runRoutineBench end-to-end
// against a stub server, executeBenchAttempt against a fake poster, and
// the render/print helpers.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// fakePoster satisfies executeBenchAttempt's minimal client interface.
type fakePoster struct {
	resp *http.Response
	err  error
}

func (f fakePoster) Post(string, any) (*http.Response, error) { return f.resp, f.err }

func covBenchResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestExecuteBenchAttempt_TransportError(t *testing.T) {
	// Transport error WITH a non-nil resp exercises the drain-close branch.
	p := fakePoster{resp: covBenchResp(200, `{}`), err: errors.New("connection reset")}
	a := executeBenchAttempt(p, covWSCli8, "slug", "", 3, nil)
	if a.Status != "ERROR" || a.Attempt != 3 || !strings.Contains(a.FailReason, "connection reset") {
		t.Errorf("transport error attempt wrong: %+v", a)
	}
}

func TestExecuteBenchAttempt_HTTPStatus(t *testing.T) {
	p := fakePoster{resp: covBenchResp(503, `{"error":"busy"}`)}
	a := executeBenchAttempt(p, covWSCli8, "slug", "", 1, nil)
	if a.Status != "HTTP_503" {
		t.Errorf("status: got %q want HTTP_503", a.Status)
	}
}

func TestExecuteBenchAttempt_DecodeError(t *testing.T) {
	p := fakePoster{resp: covBenchResp(200, `not-json`)}
	a := executeBenchAttempt(p, covWSCli8, "slug", "", 1, nil)
	if a.Status != "DECODE_ERROR" || a.FailReason == "" {
		t.Errorf("decode error attempt wrong: %+v", a)
	}
}

func TestExecuteBenchAttempt_PassAndFail(t *testing.T) {
	p := fakePoster{resp: covBenchResp(200,
		`{"run_id":"run-1","status":"COMPLETED","duration_ms":120,"cost_usd":0.01}`)}
	a := executeBenchAttempt(p, covWSCli8, "slug", "fast", 1, map[string]any{"text": "x"})
	if a.Status != "COMPLETED" || a.RunID != "run-1" || a.DurationMs != 120 || a.CostUSD != 0.01 || a.FailReason != "" {
		t.Errorf("pass attempt wrong: %+v", a)
	}

	p = fakePoster{resp: covBenchResp(200,
		`{"run_id":"run-2","status":"FAILED","error_message":"cost cap exceeded: $0.20 > $0.10"}`)}
	a = executeBenchAttempt(p, covWSCli8, "slug", "", 2, nil)
	if a.Status != "FAILED" || a.FailReason != "cost-cap" {
		t.Errorf("fail attempt wrong: %+v", a)
	}
}

func TestPrintBenchAttempt(t *testing.T) {
	var buf bytes.Buffer
	printBenchAttempt(&buf, benchAttempt{Attempt: 2, Status: "FAILED", DurationMs: 1500, CostUSD: 0.02, FailReason: "cost-cap"})
	out := buf.String()
	for _, want := range []string{"#2", "FAILED", "1500ms", "$0.0200", "(cost-cap)"} {
		if !strings.Contains(out, want) {
			t.Errorf("printBenchAttempt missing %q: %q", want, out)
		}
	}
}

func TestRenderBenchReport_Table(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	s := summariseBench("my-routine", "", []benchAttempt{
		{Attempt: 1, Status: "COMPLETED", DurationMs: 100, CostUSD: 0.01},
		{Attempt: 2, Status: "FAILED", DurationMs: 300, CostUSD: 0.03, FailReason: "cost-cap"},
		{Attempt: 3, Status: "FAILED", DurationMs: 200, CostUSD: 0.02, FailReason: "gate-fail"},
		{Attempt: 4, Status: "FAILED", DurationMs: 250, CostUSD: 0.04, FailReason: "cost-cap"},
	})

	cmd := routineBenchCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	t.Cleanup(func() { cmd.SetOut(nil) })

	if err := renderBenchReport(cmd, s); err != nil {
		t.Fatalf("renderBenchReport: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"my-routine × 4 runs (tier=(authored))",
		"Pass rate:  1/4",
		"cost-cap", "gate-fail",
		"UNRELIABLE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// Fail breakdown order: cost-cap (2) before gate-fail (1).
	if strings.Index(out, "cost-cap") > strings.Index(out, "gate-fail") {
		t.Errorf("fail reasons not count-sorted:\n%s", out)
	}
}

func TestRenderBenchReport_JSONAndYAML(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	s := summariseBench("my-routine", "fast", []benchAttempt{
		{Attempt: 1, Status: "COMPLETED", DurationMs: 100, CostUSD: 0.01},
	})

	cliCfg.Format = "json"
	out := covCaptureStdoutCli8(t, func() {
		if err := renderBenchReport(routineBenchCmd, s); err != nil {
			t.Errorf("json render: %v", err)
		}
	})
	if !strings.Contains(out, `"slug"`) || !strings.Contains(out, "my-routine") {
		t.Errorf("json report missing fields: %q", out)
	}

	cliCfg.Format = "yaml"
	out = covCaptureStdoutCli8(t, func() {
		if err := renderBenchReport(routineBenchCmd, s); err != nil {
			t.Errorf("yaml render: %v", err)
		}
	})
	if !strings.Contains(out, "slug: my-routine") {
		t.Errorf("yaml report missing slug: %q", out)
	}
}

func TestRunRoutineBench_Validation(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")

	covSetFlagCli8(t, routineBenchCmd, "runs", "0")
	err := runRoutineBench(routineBenchCmd, []string{"slug"})
	if err == nil || !strings.Contains(err.Error(), "--runs must be >= 1") {
		t.Errorf("expected runs validation; got %v", err)
	}

	covSetFlagCli8(t, routineBenchCmd, "runs", "1")
	covSetFlagCli8(t, routineBenchCmd, "inputs", "{not json")
	err = runRoutineBench(routineBenchCmd, []string{"slug"})
	if err == nil || !strings.Contains(err.Error(), "parse --inputs JSON") {
		t.Errorf("expected inputs parse error; got %v", err)
	}
}

func TestRunRoutineBench_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := runRoutineBench(routineBenchCmd, []string{"slug"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestRunRoutineBench_EndToEnd(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	// 3 runs: pass, cost-cap fail, pass.
	responses := []string{
		`{"run_id":"r1","status":"COMPLETED","duration_ms":100,"cost_usd":0.01}`,
		`{"run_id":"r2","status":"FAILED","duration_ms":50,"cost_usd":0.2,"error_message":"cost cap exceeded"}`,
		`{"run_id":"r3","status":"DEDUPED","duration_ms":120,"cost_usd":0.011}`,
	}
	call := 0
	stub.OnPost("/api/v1/workspaces/"+covWSCli8+"/pipelines/my-routine/run",
		func(_ *http.Request, _ []byte) (int, []byte, string) {
			resp := responses[call%len(responses)]
			call++
			return 200, []byte(resp), "application/json"
		})

	covSetFlagCli8(t, routineBenchCmd, "runs", "3")
	covSetFlagCli8(t, routineBenchCmd, "tier-override", "fast")
	covSetFlagCli8(t, routineBenchCmd, "inputs", `{"text":"hi"}`)

	var buf bytes.Buffer
	routineBenchCmd.SetOut(&buf)
	routineBenchCmd.SetErr(&buf)
	t.Cleanup(func() {
		routineBenchCmd.SetOut(nil)
		routineBenchCmd.SetErr(nil)
	})

	if err := runRoutineBench(routineBenchCmd, []string{"my-routine"}); err != nil {
		t.Fatalf("runRoutineBench: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/workspaces/"+covWSCli8+"/pipelines/my-routine/run")
	if len(calls) != 3 {
		t.Fatalf("expected 3 run POSTs, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["tier_override"] != "fast" {
		t.Errorf("tier_override not forwarded: %v", body)
	}
	inputs, _ := body["inputs"].(map[string]any)
	if inputs["text"] != "hi" {
		t.Errorf("inputs not forwarded: %v", body)
	}

	out := buf.String()
	// 2/3 ≈ 67% pass sits below the 70% FLAKY floor → UNRELIABLE.
	for _, want := range []string{"Benching my-routine × 3 runs (tier=fast)", "Pass rate:  2/3", "cost-cap", "UNRELIABLE"} {
		if !strings.Contains(out, want) {
			t.Errorf("bench output missing %q:\n%s", want, out)
		}
	}
}

func TestRunRoutineBench_NoWorkspace(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := runRoutineBench(routineBenchCmd, []string{"slug"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestSummariseBench_EmptyAttempts(t *testing.T) {
	s := summariseBench("slug", "", nil)
	if s.Runs != 0 || s.Pass != 0 || s.PassRate != 0 {
		t.Errorf("empty summary wrong: %+v", s)
	}
	if readinessVerdict(s) != "INSUFFICIENT_DATA" {
		t.Errorf("verdict for empty: %q", readinessVerdict(s))
	}
}

func TestRenderBenchReport_FailReasonTieAlphabetical(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	s := summariseBench("tie-routine", "", []benchAttempt{
		{Attempt: 1, Status: "FAILED", FailReason: "gate-fail"},
		{Attempt: 2, Status: "FAILED", FailReason: "cost-cap"},
	})

	var buf bytes.Buffer
	routineBenchCmd.SetOut(&buf)
	t.Cleanup(func() { routineBenchCmd.SetOut(nil) })

	if err := renderBenchReport(routineBenchCmd, s); err != nil {
		t.Fatalf("renderBenchReport: %v", err)
	}
	out := buf.String()
	// Equal counts → alphabetical: cost-cap before gate-fail.
	if strings.Index(out, "cost-cap") > strings.Index(out, "gate-fail") {
		t.Errorf("tie not broken alphabetically:\n%s", out)
	}
}

// Cooldown branch: 2 runs with a 10ms cooldown — the loop must still
// fire both POSTs.
func TestRunRoutineBench_Cooldown(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/workspaces/"+covWSCli8+"/pipelines/cool/run",
		clitest.JSONResponse(200, map[string]any{"run_id": "r", "status": "COMPLETED"}))

	covSetFlagCli8(t, routineBenchCmd, "runs", "2")
	covSetFlagCli8(t, routineBenchCmd, "cooldown-ms", "10")

	var buf bytes.Buffer
	routineBenchCmd.SetOut(&buf)
	routineBenchCmd.SetErr(&buf)
	t.Cleanup(func() {
		routineBenchCmd.SetOut(nil)
		routineBenchCmd.SetErr(nil)
	})

	if err := runRoutineBench(routineBenchCmd, []string{"cool"}); err != nil {
		t.Fatalf("runRoutineBench: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/workspaces/"+covWSCli8+"/pipelines/cool/run"); len(calls) != 2 {
		t.Errorf("expected 2 POSTs with cooldown, got %d", len(calls))
	}
	if !strings.Contains(buf.String(), "PRODUCTION_READY") {
		t.Errorf("2/2 pass should be PRODUCTION_READY:\n%s", buf.String())
	}
}

func TestRunRoutineBench_FailFast(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/workspaces/"+covWSCli8+"/pipelines/my-routine/run",
		clitest.JSONResponse(200, map[string]any{
			"run_id": "r1", "status": "FAILED", "error_message": "outcomes failed: rubric",
		}))

	covSetFlagCli8(t, routineBenchCmd, "runs", "5")
	covSetFlagCli8(t, routineBenchCmd, "fail-fast", "true")

	var buf bytes.Buffer
	routineBenchCmd.SetOut(&buf)
	routineBenchCmd.SetErr(&buf)
	t.Cleanup(func() {
		routineBenchCmd.SetOut(nil)
		routineBenchCmd.SetErr(nil)
	})

	if err := runRoutineBench(routineBenchCmd, []string{"my-routine"}); err != nil {
		t.Fatalf("runRoutineBench: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/workspaces/"+covWSCli8+"/pipelines/my-routine/run"); len(calls) != 1 {
		t.Errorf("fail-fast must stop after 1 attempt; got %d", len(calls))
	}
	if !strings.Contains(buf.String(), "fail-fast: aborting") {
		t.Errorf("missing fail-fast notice:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "BROKEN") {
		t.Errorf("0%% pass should be BROKEN verdict:\n%s", buf.String())
	}
}
