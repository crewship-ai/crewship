package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestSystemCmdStructure(t *testing.T) {
	t.Parallel()

	if systemCmd.Use != "system" {
		t.Errorf("system Use: got %q, want %q", systemCmd.Use, "system")
	}
	if !strings.Contains(strings.ToLower(systemCmd.Short), "system") {
		t.Errorf("system Short should mention system; got %q", systemCmd.Short)
	}

	have := map[string]bool{}
	for _, sub := range systemCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"info", "keeper", "stats", "onboarding", "aux-status"} {
		if !have[want] {
			t.Errorf("system missing subcommand %q; have %v", want, have)
		}
	}
}

func TestSystemAuxStatusCmdStructure(t *testing.T) {
	t.Parallel()

	if systemAuxStatusCmd.Use != "aux-status" {
		t.Errorf("aux-status Use: got %q, want %q", systemAuxStatusCmd.Use, "aux-status")
	}
	if !strings.Contains(strings.ToLower(systemAuxStatusCmd.Short), "auxiliary") {
		t.Errorf("aux-status Short should mention auxiliary; got %q", systemAuxStatusCmd.Short)
	}
	if !strings.Contains(systemAuxStatusCmd.Long, "aux-status") {
		t.Errorf("aux-status Long should reference command name; got %q", systemAuxStatusCmd.Long)
	}
}

func TestSystemAuxStatusRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

// systemAuxCurrentShape is the payload GET /api/v1/system/aux-status has
// returned since #1506: one `subsystems` row per evaluator, each carrying
// the health (configured + buildable) and reachability (did the model
// server answer just now) verdicts that replaced the old flat slot list.
//
// It covers the four states the table has to tell apart: reachable,
// unreachable, unhealthy, and unprobed (a paid API the server refuses to
// dial just to render a status page).
const systemAuxCurrentShape = `{"subsystems":[
	{"id":"access_gatekeeper","label":"Credential access judge","provider":"ollama","model":"phi3:mini","source":"keeper_config","healthy":true,"reachable":false,"reach_detail":"no response from http://localhost:11434"},
	{"id":"curator","label":"Skill review + memory consolidation","provider":"ollama","model":"phi3:mini","timeout_ms":30000,"source":"explicit","healthy":true,"reachable":true},
	{"id":"behavior","label":"Tool-call behaviour monitor","provider":"anthropic","model":"claude-haiku-4-5","timeout_ms":8000,"source":"fallback","healthy":true,"reach_detail":"not probed — Crewship does not call a paid API to render a status page"},
	{"id":"negative","label":"Failure → lessons extraction","source":"unconfigured","healthy":false,"detail":"no provider configured for slot negative and no fallback"}
]}`

// systemAuxMock stubs GET /api/v1/system/aux-status with a deterministic
// current-shape payload so the CLI's table rendering and JSON pass-through
// can be exercised without standing up the full server.
type systemAuxMock struct {
	t       *testing.T
	mu      sync.Mutex
	called  bool
	path    string
	query   string
	resBody string
}

func (m *systemAuxMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/aux-status" {
			m.t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.called = true
		m.path = r.URL.Path
		m.query = r.URL.RawQuery
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body := m.resBody
		if body == "" {
			body = systemAuxCurrentShape
		}
		_, _ = w.Write([]byte(body))
	})
}

// auxTestCfg is a logged-in config pointed at srv with a workspace set —
// aux-status sits behind authedAdmin (RequireWorkspace), so a workspace-less
// invocation is a 400, not a listing.
func auxTestCfg(server string) *cli.CLIConfig {
	return &cli.CLIConfig{
		Token:     "fake-token",
		Server:    server,
		Workspace: "c0000000000000000000001",
	}
}

func TestSystemAuxStatusRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	if err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.called {
		t.Fatal("aux-status endpoint not called")
	}
	if m.path != "/api/v1/system/aux-status" {
		t.Errorf("path = %q, want /api/v1/system/aux-status", m.path)
	}
}

// TestSystemAuxStatusRunE_RendersSubsystems is the regression the reshape
// slipped past: against a current server the command must render the
// `subsystems` rows — including whether each one ANSWERS — not an empty
// table decoded off a field the server stopped sending.
func TestSystemAuxStatusRunE_RendersSubsystems(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if strings.Contains(out, "no results") {
		t.Fatalf("current-shape response rendered as an empty table:\n%s", out)
	}
	for _, want := range []string{
		"access_gatekeeper", "curator", "behavior", "negative",
		"phi3:mini", "claude-haiku-4-5", "30.0s",
		"explicit", "fallback", "unconfigured",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// The point of the reshape: a subsystem that is configured but silent
	// must not read the same as one that answers.
	for _, want := range []string{"unreachable", "unhealthy", "unprobed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing reachability verdict %q:\n%s", want, out)
		}
	}
	// A verdict with no reason is not actionable — the server's own words
	// have to reach the operator.
	for _, want := range []string{
		"no response from http://localhost:11434",
		"no provider configured for slot negative",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing server detail %q:\n%s", want, out)
		}
	}
}

// TestSystemAuxStatusRunE_UnprobedReasonNotRepeated pins the reason block to
// reasons that are actually about this row's problem.
//
// A paid-API slot that fails to BUILD — the everyday "no ANTHROPIC_API_KEY"
// case — comes back healthy=false with the build error in `detail`, and the
// server also stamps the standing policy note into `reach_detail` ("not
// probed — Crewship does not call a paid API…") for every non-self-hosted
// row, probed or not. Printing both put the policy note directly under the
// real error, where it reads as a second fault and invites the operator to
// go looking for a probe that was never the problem.
//
// `reach_detail` is only evidence when a probe actually happened, i.e. when
// `reachable` is non-nil. Unprobed rows carry their state in the status word.
func TestSystemAuxStatusRunE_UnprobedReasonNotRepeated(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t, resBody: `{"subsystems":[
		{"id":"curator","label":"Skill review","provider":"anthropic","model":"claude-haiku-4-5","timeout_ms":30000,"source":"fallback","healthy":false,"detail":"anthropic: no API key configured","reach_detail":"not probed — Crewship does not call a paid API to render a status page"}
	]}`}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// The real fault still has to reach the operator.
	if !strings.Contains(out, "anthropic: no API key configured") {
		t.Errorf("build failure must be reported:\n%s", out)
	}
	// The policy note must not be listed as one of this row's faults.
	if strings.Contains(out, "not probed") {
		t.Errorf("unprobed policy note must not be listed as a fault reason:\n%s", out)
	}
}

// TestSystemAuxStatusRunE_ProbedReasonStillShown is the other half of the
// pair above: when a probe DID run and failed, `reach_detail` is the whole
// finding and must survive.
func TestSystemAuxStatusRunE_ProbedReasonStillShown(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t, resBody: `{"subsystems":[
		{"id":"access_gatekeeper","label":"Credential access judge","provider":"ollama","model":"phi3","source":"keeper_config","healthy":true,"reachable":false,"reach_detail":"no response from http://localhost:11434"}
	]}`}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "no response from http://localhost:11434") {
		t.Errorf("a failed probe is the whole finding and must be printed:\n%s", out)
	}
}

// TestSystemAuxStatusRunE_JSONPassThrough keeps `--format json` a stable
// jq target on the CURRENT key: pipelines filtering `.slots[]` broke at
// #1506 and must be pointed at `.subsystems[]`.
func TestSystemAuxStatusRunE_JSONPassThrough(t *testing.T) {
	saveCLIState(t)
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })

	m := &systemAuxMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var decoded struct {
		Subsystems []struct {
			ID        string `json:"id"`
			Reachable *bool  `json:"reachable"`
		} `json:"subsystems"`
	}
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("--format json is not valid JSON (%v):\n%s", jerr, out)
	}
	if len(decoded.Subsystems) != 4 {
		t.Fatalf("subsystems = %d, want 4:\n%s", len(decoded.Subsystems), out)
	}
	if decoded.Subsystems[0].Reachable == nil || *decoded.Subsystems[0].Reachable {
		t.Errorf("reachable=false must survive into --format json:\n%s", out)
	}
	// The unprobed row keeps its honest third state rather than collapsing
	// to false, which would read as "the model server is down".
	if decoded.Subsystems[2].Reachable != nil {
		t.Errorf("unprobed subsystem must stay null in JSON, got %v:\n%s", *decoded.Subsystems[2].Reachable, out)
	}
}

// TestSystemAuxStatusRunE_LegacyServerErrors pins the old-server contract:
// a pre-#1506 server still answers 200 with `{"slots": …}`, and printing an
// empty table off it would read as "nothing configured". Erroring names the
// real problem.
func TestSystemAuxStatusRunE_LegacyServerErrors(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t, resBody: `{"slots":[
		{"slot":"curator","provider":"anthropic","model":"claude-haiku-4-5","timeout_ms":30000,"source":"explicit"}
	]}`}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil {
		t.Fatal("expected an error against a pre-reshape server, got an empty table")
	}
	if !strings.Contains(err.Error(), "Upgrade the server") || !strings.Contains(err.Error(), "subsystems") {
		t.Errorf("error should name the stale server and the fix; got %v", err)
	}
}

// TestSystemAuxStatusRunE_MissingSubsystemsErrors covers the other unknown
// shape: a 200 with neither key. Same reasoning — never print "(no results)"
// for a response we did not understand.
func TestSystemAuxStatusRunE_MissingSubsystemsErrors(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t, resBody: `{}`}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "subsystems") {
		t.Errorf("expected a 'no subsystems list' error; got %v", err)
	}
}

// TestSystemAuxStatusRunE_SendsWorkspace: the route moved behind
// authedAdmin (RequireWorkspace) in #868, so dropping the workspace makes
// every call a 400 "workspace_id is required".
func TestSystemAuxStatusRunE_SendsWorkspace(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	if err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.Contains(m.query, "workspace_id=c0000000000000000000001") {
		t.Errorf("query = %q, want workspace_id", m.query)
	}
}

func TestSystemAuxStatusRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: "http://127.0.0.1:1"}

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no workspace set") {
		t.Errorf("expected 'no workspace set'; got %v", err)
	}
}

func TestSystemAuxStatusRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	cliCfg = auxTestCfg(srv.URL)

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil {
		t.Fatal("expected server error to bubble up")
	}
}

func TestDashIfEmpty(t *testing.T) {
	t.Parallel()
	if got := dashIfEmpty(""); got != "—" {
		t.Errorf("dashIfEmpty(\"\") = %q, want em-dash", got)
	}
	if got := dashIfEmpty("anthropic"); got != "anthropic" {
		t.Errorf("dashIfEmpty(%q) = %q, want passthrough", "anthropic", got)
	}
}
