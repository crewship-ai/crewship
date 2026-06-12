package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covWSCli5 is a CUID-shaped workspace id (≥21 chars, 'c' + [a-z0-9]) so the
// client injects it verbatim without a slug-resolution round trip.
const covWSCli5 = "c0000000000000000000000"

// covSetupCli5 wires the standard fixture for RunE tests: snapshots the global
// CLI state, neutralises env overrides, and points cliCfg at a fresh
// clitest stub server. Returns the stub for route registration and call
// assertions. Tests using this must NOT call t.Parallel() (global state).
func covSetupCli5(t *testing.T) *clitest.StubServer {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	origFormat := flagFormat
	flagFormat = ""
	t.Cleanup(func() { flagFormat = origFormat })
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWSCli5, Server: stub.URL()}
	return stub
}

// covCaptureStdoutCli5 redirects os.Stdout while fn runs and returns everything
// printed. Not parallel-safe — callers must not use t.Parallel().
func covCaptureStdoutCli5(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	defer func() { os.Stdout = orig }()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// covCaptureAll captures stdout AND stderr while fn runs — needed for
// flows that mix fmt.Printf (stdout) with cli.PrintSuccess/PrintWarning
// (stderr). Returns the two streams concatenated (stdout first).
func covCaptureAll(t *testing.T, fn func()) string {
	t.Helper()
	origErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	defer func() { os.Stderr = origErr }()
	stdout := covCaptureStdoutCli5(t, fn)
	_ = w.Close()
	os.Stderr = origErr
	return stdout + <-done
}

// covSwapStdin replaces os.Stdin with a pipe pre-filled with content
// (writer closed immediately so EOF follows the content).
func covSwapStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if content != "" {
		if _, err := w.WriteString(content); err != nil {
			t.Fatalf("write stdin pipe: %v", err)
		}
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

// covSetFlagCli5 sets a Cobra flag and restores its previous value at test end —
// commands are package-level singletons, so flag values leak across tests
// unless restored.
func covSetFlagCli5(t *testing.T, cmd *cobra.Command, name, val string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("flag --%s not registered on %s", name, cmd.Name())
	}
	orig := f.Value.String()
	if err := cmd.Flags().Set(name, val); err != nil {
		t.Fatalf("set --%s=%s: %v", name, val, err)
	}
	t.Cleanup(func() { _ = cmd.Flags().Set(name, orig) })
}

// ─── str ─────────────────────────────────────────────────────────────────

func TestStrCoercion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"float", 1.5, "1.5"},
		{"bool", true, "true"},
	}
	for _, tc := range cases {
		if got := str(tc.in); got != tc.want {
			t.Errorf("str(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ─── render helpers ──────────────────────────────────────────────────────

func TestRenderMe_TableFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "table"

	missions := []map[string]any{{"id": "mis_1", "title": "Fix login"}}
	approvals := []map[string]any{{"id": "apr_1", "title": "Deploy", "status": "pending"}}
	runs := []map[string]any{{"id": "run_1", "agent_slug": "viktor", "status": "DONE"}}

	out := covCaptureStdoutCli5(t, func() {
		if err := renderMe(missions, approvals, runs, nil); err != nil {
			t.Errorf("renderMe: %v", err)
		}
	})
	for _, want := range []string{"Your missions", "mis_1", "Fix login", "apr_1", "pending", "run_1", "viktor"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got:\n%s", want, out)
		}
	}
}

func TestRenderMe_JSONFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "json"

	out := covCaptureStdoutCli5(t, func() {
		if err := renderMe(
			[]map[string]any{{"id": "mis_1"}},
			nil, nil, []string{"runs: boom"},
		); err != nil {
			t.Errorf("renderMe: %v", err)
		}
	})
	var v struct {
		Missions []map[string]any `json:"missions"`
		Errors   []string         `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(v.Missions) != 1 || v.Missions[0]["id"] != "mis_1" {
		t.Errorf("missions = %v, want one with id mis_1", v.Missions)
	}
	if len(v.Errors) != 1 || v.Errors[0] != "runs: boom" {
		t.Errorf("errors = %v", v.Errors)
	}
}

func TestRenderToday_TableFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "table"

	runs := []map[string]any{
		{"id": "r1", "status": "DONE"},
		{"id": "r2", "status": "DONE"},
		{"id": "r3", "status": "FAILED"},
	}
	out := covCaptureStdoutCli5(t, func() {
		if err := renderToday(runs, map[string]any{"rows": []any{}}, nil); err != nil {
			t.Errorf("renderToday: %v", err)
		}
	})
	for _, want := range []string{"Runs:", "3", "DONE × 2", "FAILED × 1", "Cost (last 24h)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestRenderToday_JSONFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "json"

	out := covCaptureStdoutCli5(t, func() {
		if err := renderToday(nil, nil, nil); err != nil {
			t.Errorf("renderToday: %v", err)
		}
	})
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if _, ok := v["runs"]; !ok {
		t.Errorf("JSON output missing runs key: %v", v)
	}
}

func TestRenderNow_TableFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "table"

	runs := []map[string]any{{"id": "r1", "agent_slug": "eva", "started_at": "2026-06-12T08:00:00Z"}}
	agents := []map[string]any{
		{"slug": "eva", "status": "running"},
		{"slug": "viktor", "status": "idle"},
		{"slug": "petra", "status": "busy"},
	}
	approvals := []map[string]any{{"id": "apr_9", "title": "Spend cap"}}

	out := covCaptureStdoutCli5(t, func() {
		if err := renderNow(runs, agents, approvals, []string{"agents: partial"}); err != nil {
			t.Errorf("renderNow: %v", err)
		}
	})
	for _, want := range []string{"Running missions/runs:", "r1", "1 idle, 2 busy", "Pending approvals:", "apr_9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestRenderNow_JSONFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "json"

	out := covCaptureStdoutCli5(t, func() {
		if err := renderNow(nil, nil, nil, nil); err != nil {
			t.Errorf("renderNow: %v", err)
		}
	})
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if _, ok := v["running_runs"]; !ok {
		t.Errorf("JSON output missing running_runs key: %v", v)
	}
}

// ─── meCmd ───────────────────────────────────────────────────────────────

func TestMeCmdRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}

	err := meCmd.RunE(meCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestMeCmdRunE_HappyPath(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"

	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "mis_1", "title": "Ship it"}},
	}))
	stub.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "apr_1", "title": "Approve", "status": "pending"}},
	}))
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "run_1", "agent_slug": "eva", "status": "DONE"}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = meCmd.RunE(meCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"mis_1", "apr_1", "run_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	for _, path := range []string{"/api/v1/missions", "/api/v1/approvals", "/api/v1/runs"} {
		if got := stub.CallsFor("GET", path); len(got) == 0 {
			t.Errorf("expected at least one GET %s", path)
		}
	}
}

func TestMeCmdRunE_BareArrayFallback(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"

	// missions returns a bare array — exercises the second-decode fallback.
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200,
		[]map[string]any{{"id": "mis_bare", "title": "Bare"}}))
	stub.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = meCmd.RunE(meCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "mis_bare") {
		t.Errorf("bare-array mission did not surface; got:\n%s", out)
	}
}

func TestMeCmdRunE_SessionExpired(t *testing.T) {
	stub := covSetupCli5(t)

	expired := clitest.ErrorResponse(401, "session_invalid")
	stub.OnGet("/api/v1/missions", expired)
	stub.OnGet("/api/v1/approvals", expired)
	stub.OnGet("/api/v1/runs", expired)

	err := meCmd.RunE(meCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Errorf("expected session-expired error; got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "crewship login") {
		t.Errorf("remediation hint missing; got %v", err)
	}
}

// ─── todayCmd ────────────────────────────────────────────────────────────

func TestTodayCmdRunE_HappyPath(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"

	today := time.Now().UTC().Format("2006-01-02")
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "run_today", "status": "DONE", "created_at": today + "T01:00:00Z"},
			{"id": "run_old", "status": "DONE", "created_at": "2001-01-01T01:00:00Z"},
		},
	}))
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"scope_kind": "crew", "cost_usd": 1.5}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = todayCmd.RunE(todayCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var v struct {
		Runs []map[string]any `json:"runs"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &v); jsonErr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jsonErr, out)
	}
	if len(v.Runs) != 1 || v.Runs[0]["id"] != "run_today" {
		t.Errorf("expected only today's run; got %v", v.Runs)
	}
	if calls := stub.CallsFor("GET", "/api/v1/runs"); len(calls) != 1 {
		t.Errorf("expected exactly 1 GET /api/v1/runs, got %d", len(calls))
	} else if !strings.Contains(calls[0].Query, "limit=100") {
		t.Errorf("runs query missing limit=100: %q", calls[0].Query)
	}
}

func TestTodayCmdRunE_PartialErrorsStillRender(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"

	stub.OnGet("/api/v1/runs", clitest.ErrorResponse(500, "boom"))
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.ErrorResponse(500, "boom"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = todayCmd.RunE(todayCmd, nil) })
	if err != nil {
		t.Fatalf("partial failures must not abort; got %v", err)
	}
	var v struct {
		Errors []string `json:"errors"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &v); jsonErr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jsonErr, out)
	}
	if len(v.Errors) != 2 {
		t.Errorf("expected 2 recorded errors, got %v", v.Errors)
	}
}

func TestTodayCmdRunE_SessionExpired(t *testing.T) {
	stub := covSetupCli5(t)

	expired := clitest.ErrorResponse(401, "session_invalid")
	stub.OnGet("/api/v1/runs", expired)
	stub.OnGet("/api/v1/paymaster/top-spenders", expired)

	err := todayCmd.RunE(todayCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Errorf("expected session-expired error; got %v", err)
	}
}

// ─── nowCmd ──────────────────────────────────────────────────────────────

func TestNowCmdRunE_HappyPath(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"

	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "run_live", "agent_slug": "eva", "started_at": "x"}},
	}))
	// agents returns a bare array — covers the fallback decode in fetchData.
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200,
		[]map[string]any{{"slug": "eva", "status": "running"}}))
	stub.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "apr_2", "title": "Cap"}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = nowCmd.RunE(nowCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"run_live", "apr_2", "eva"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	if calls := stub.CallsFor("GET", "/api/v1/runs"); len(calls) != 1 {
		t.Fatalf("expected 1 GET /api/v1/runs, got %d", len(calls))
	} else if !strings.Contains(calls[0].Query, "status=RUNNING") {
		t.Errorf("runs query missing status=RUNNING: %q", calls[0].Query)
	}
}

func TestNowCmdRunE_SessionExpired(t *testing.T) {
	stub := covSetupCli5(t)

	expired := clitest.ErrorResponse(401, "session_invalid")
	stub.OnGet("/api/v1/runs", expired)
	stub.OnGet("/api/v1/agents", expired)
	stub.OnGet("/api/v1/approvals", expired)

	err := nowCmd.RunE(nowCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Errorf("expected session-expired error; got %v", err)
	}
}
