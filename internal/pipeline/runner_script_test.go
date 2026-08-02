package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeScriptRunner captures the last request and returns a canned result,
// so the runner's contract (interpreter inference, path resolution, input
// env, arg rendering, exit-code handling) can be asserted without a real
// container.
type fakeScriptRunner struct {
	last   ScriptRunRequest
	result ScriptRunResult
	err    error
}

func (f *fakeScriptRunner) RunScript(_ context.Context, req ScriptRunRequest) (ScriptRunResult, error) {
	f.last = req
	return f.result, f.err
}

func TestScriptStep_RunsAndRendersInputs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: `{"ucet":"123","soucet_ok":true}`, ExitCode: 0}}
	exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)

	step := Step{
		ID:   "parse",
		Type: StepScript,
		Script: &ScriptStep{
			Path: "scripts/parse_vypis.py",
			Args: []string{"{{ inputs.file }}"},
			Env:  map[string]string{"MODE": "{{ inputs.mode }}"},
		},
	}
	rc := RenderContext{Inputs: map[string]any{"file": "vypis.pdf", "mode": "strict"}}
	out, _, _, err := exec.runScriptStep(context.Background(), step, rc, RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"}, "run_1")
	if err != nil {
		t.Fatalf("script step: %v", err)
	}
	if !strings.Contains(out, `"soucet_ok":true`) {
		t.Fatalf("stdout not returned as step output: %q", out)
	}
	if fake.last.Interpreter != "python3" {
		t.Errorf("interpreter = %q, want python3 (inferred from .py)", fake.last.Interpreter)
	}
	if fake.last.Path != "/crew/shared/scripts/parse_vypis.py" {
		t.Errorf("path = %q, want /crew/shared/scripts/parse_vypis.py", fake.last.Path)
	}
	if len(fake.last.Args) != 1 || fake.last.Args[0] != "vypis.pdf" {
		t.Errorf("args = %v, want [vypis.pdf]", fake.last.Args)
	}
	if fake.last.Env["CREWSHIP_INPUT_FILE"] != "vypis.pdf" {
		t.Errorf("CREWSHIP_INPUT_FILE = %q, want vypis.pdf", fake.last.Env["CREWSHIP_INPUT_FILE"])
	}
	if fake.last.Env["MODE"] != "strict" {
		t.Errorf("MODE env = %q, want strict (rendered)", fake.last.Env["MODE"])
	}
	if fake.last.WorkspaceID != "ws_1" || fake.last.AuthorCrewID != "crew_1" {
		t.Errorf("crew scope not threaded: ws=%q crew=%q", fake.last.WorkspaceID, fake.last.AuthorCrewID)
	}
}

func TestScriptStep_ExplicitInterpreterAndAbsolutePath(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "ok", ExitCode: 0}}
	exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)
	step := Step{ID: "run", Type: StepScript, Script: &ScriptStep{
		Path:        "/crew/shared/bin/reconcile",
		Interpreter: "bash",
	}}
	if _, _, _, err := exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{}, "run_1"); err != nil {
		t.Fatalf("script step: %v", err)
	}
	if fake.last.Interpreter != "bash" {
		t.Errorf("interpreter = %q, want bash (explicit)", fake.last.Interpreter)
	}
	if fake.last.Path != "/crew/shared/bin/reconcile" {
		t.Errorf("path = %q, want /crew/shared/bin/reconcile", fake.last.Path)
	}
}

func TestScriptStep_NonZeroExitFails(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "partial", Stderr: "Traceback: boom", ExitCode: 1}}
	exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)
	step := Step{ID: "parse", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py"}}
	out, _, _, err := exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{}, "run_1")
	if err == nil {
		t.Fatal("expected error on exit code 1")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should surface stderr, got: %v", err)
	}
	if out != "partial" {
		t.Errorf("partial stdout should still be returned, got %q", out)
	}
}

func TestScriptStep_NotWired(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil) // no WithScriptRunner
	step := Step{ID: "parse", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py"}}
	_, _, _, err := exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{}, "run_1")
	if err == nil || !strings.Contains(err.Error(), "no ScriptRunner wired") {
		t.Fatalf("expected 'no ScriptRunner wired' error, got %v", err)
	}
}

func TestValidateScriptStep(t *testing.T) {
	cases := []struct {
		name string
		s    *ScriptStep
		ok   bool
	}{
		{"valid python by ext", &ScriptStep{Path: "scripts/parse.py"}, true},
		{"valid explicit interpreter", &ScriptStep{Path: "bin/run", Interpreter: "bash"}, true},
		{"valid absolute under shared", &ScriptStep{Path: "/crew/shared/scripts/x.py"}, true},
		{"missing body path", &ScriptStep{Path: ""}, false},
		{"traversal escapes shared", &ScriptStep{Path: "../../etc/passwd"}, false},
		{"absolute outside crew", &ScriptStep{Path: "/etc/passwd"}, false},
		{"unknown ext no interpreter", &ScriptStep{Path: "scripts/mystery"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStepEgress(Step{ID: "s", Type: StepScript, Script: tc.s})
			if tc.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error for %+v", tc.s)
			}
		})
	}
	// nil body
	if err := validateStepEgress(Step{ID: "s", Type: StepScript}); err == nil {
		t.Error("expected error for nil script body")
	}
}

// scriptCovContainer builds on the orchCovContainer fake, overriding Exec +
// ExecInspect so the prod RunScript path (timeout, exit-code trust, inspect
// failure) is testable without Docker.
type scriptCovContainer struct {
	orchCovContainer
	execReader   io.ReadCloser
	execErr      error
	inspectCode  int
	inspectErr   error
	lastExecCfg  provider.ExecConfig
	inspectCalls int
	// onExec fires inside the exec, i.e. while the script is "running", so a
	// test can observe state that is only true for the duration of the step.
	onExec func()
}

func (m *scriptCovContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	m.lastExecCfg = cfg
	if m.onExec != nil {
		m.onExec()
	}
	if m.execErr != nil {
		return nil, m.execErr
	}
	r := m.execReader
	if r == nil {
		r = io.NopCloser(strings.NewReader("out"))
	}
	return &provider.ExecResult{ExecID: "exec-script", Reader: r}, nil
}

func (m *scriptCovContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	m.inspectCalls++
	return false, m.inspectCode, m.inspectErr
}

func newScriptTestRunner(c provider.ContainerProvider) *OrchestratorRunner {
	return &OrchestratorRunner{container: c, logger: slog.Default()}
}

// Defect 2 (CodeRabbit critical): a failed ExecInspect must NOT be trusted as
// exit 0 — that silently converts an unknown outcome into a false SUCCESS.
func TestRunScript_InspectErrorIsNotSuccess(t *testing.T) {
	c := &scriptCovContainer{inspectErr: errors.New("daemon gone")}
	r := newScriptTestRunner(c)
	_, err := r.RunScript(context.Background(), ScriptRunRequest{
		AuthorCrewID: "crew_1", Interpreter: "python3", Path: "/crew/shared/s.py",
	})
	if err == nil {
		t.Fatal("inspect failure must surface as an error, not exit 0 success")
	}
	if !strings.Contains(err.Error(), "inspect") {
		t.Errorf("error should say exec inspection failed, got: %v", err)
	}
}

// Defect 1: TimeoutSec must be ENFORCED — a script that never finishes
// (blocking reader) must fail within the timeout, not hang the executor
// goroutine forever.
func TestRunScript_TimeoutEnforced(t *testing.T) {
	pr, _ := io.Pipe() // reader that never delivers data and never EOFs
	c := &scriptCovContainer{execReader: pr}
	r := newScriptTestRunner(c)
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := r.RunScript(context.Background(), ScriptRunRequest{
			AuthorCrewID: "crew_1", Interpreter: "bash", Path: "/crew/shared/hang.sh",
			TimeoutSec: 1,
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error should say the script timed out, got: %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("timeout took %v, want ~1s", time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunScript hung — TimeoutSec not enforced")
	}
}

// Defect 3 (CodeRabbit major): when the runner errors before the process ran,
// the audit entry must not claim exit_code 0 — that reads as a successful run.
func TestScriptAudit_RunnerErrorNotExitZero(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	em := &captureEmitter{}
	fake := &fakeScriptRunner{err: errors.New("ensure container: boom")}
	exec := NewExecutor(store, resolver, nil, em).WithScriptRunner(fake)
	step := Step{ID: "parse", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py"}}
	_, _, _, _ = exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{WorkspaceID: "ws"}, "run_1")

	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.entries {
		if e.Type != journal.EntryExecCommand {
			continue
		}
		if code, ok := e.Payload["exit_code"].(int); ok && code == 0 {
			t.Fatalf("audit claims exit_code 0 for an exec that never ran: %+v", e.Payload)
		}
		if _, hasErr := e.Payload["error"]; !hasErr {
			t.Errorf("audit for a failed exec should carry the error, payload: %+v", e.Payload)
		}
		return
	}
	t.Fatal("no exec.command audit entry emitted")
}

// Defect 4 (CodeRabbit major): rendered args can carry secrets (an input
// templated into args) — the journaled command must be scrubbed.
func TestScriptAudit_CommandScrubbed(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	em := &captureEmitter{}
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "ok", ExitCode: 0}}
	exec := NewExecutor(store, resolver, nil, em).WithScriptRunner(fake)
	secret := "sk-ant-api03-AAAABBBBCCCCDDDDEEEE1234"
	step := Step{ID: "call", Type: StepScript, Script: &ScriptStep{
		Path: "scripts/x.py", Args: []string{"--token", "{{ inputs.tok }}"},
	}}
	rc := RenderContext{Inputs: map[string]any{"tok": secret}}
	_, _, _, err := exec.runScriptStep(context.Background(), step, rc, RunInput{WorkspaceID: "ws"}, "run_1")
	if err != nil {
		t.Fatalf("script step: %v", err)
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.entries {
		if e.Type != journal.EntryExecCommand {
			continue
		}
		cmd, _ := e.Payload["command"].(string)
		if strings.Contains(cmd, secret) {
			t.Fatalf("journaled command leaks the secret: %q", cmd)
		}
		if strings.Contains(e.Summary, secret) {
			t.Fatalf("journal summary leaks the secret: %q", e.Summary)
		}
		return
	}
	t.Fatal("no exec.command audit entry emitted")
}

// #1473: a routine `script` step runs inside the agent container but built its
// exec environment from req.Env alone, so it inherited no HTTP_PROXY. The crew
// egress allowlist is enforced by the sidecar proxy, so a step with no proxy
// env is not merely bypassing the fence — it never meets it. Found live: a
// `restricted` crew allowlisted to httpbin.org reached example.org with a
// plain curl from a script step.
//
// The env must come from the same source the agent-process path uses, so the
// two in-container paths cannot drift apart again.
func TestRunScript_ExecEnvCarriesSidecarProxy(t *testing.T) {
	c := &scriptCovContainer{}
	r := newScriptTestRunner(c)
	if _, err := r.RunScript(context.Background(), ScriptRunRequest{
		AuthorCrewID: "crew_1", Interpreter: "bash", Path: "/crew/shared/s.sh",
		Env: map[string]string{"CREWSHIP_INPUT_X": "1"},
	}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	got := make(map[string]string, len(c.lastExecCfg.Env))
	for _, kv := range c.lastExecCfg.Env {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			got[kv[:eq]] = kv[eq+1:]
		}
	}

	// Every variable the agent path sets, in both cases — Go honours the
	// upper-case pair, most CLIs (curl, wget) read the lower-case one, and a
	// script step is exactly where a bare `curl` runs.
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if got[k] != "http://127.0.0.1:9119" {
			t.Errorf("%s = %q, want the sidecar proxy — without it the crew egress allowlist never applies to script steps (#1473)", k, got[k])
		}
	}
	// NO_PROXY keeps loopback direct; without it a script's call to the sidecar
	// itself (or a localhost health check) proxies through the sidecar and loops.
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if got[k] == "" {
			t.Errorf("%s is unset — loopback must stay direct or the proxy recurses into itself", k)
		}
	}
	// The step's own env must survive alongside the platform vars.
	if got["CREWSHIP_INPUT_X"] != "1" {
		t.Errorf("declared input dropped: CREWSHIP_INPUT_X = %q, want 1", got["CREWSHIP_INPUT_X"])
	}
}

// A step must not be able to unset its own fence by declaring a proxy variable
// in `script.env` — the platform value wins. Otherwise the fix is one line of
// routine DSL away from being undone, by exactly the actor it constrains.
//
// The mixed-case and ALL_PROXY cases are not paranoia. CPython's
// urllib.request.getproxies_environment() lowercases every env name before
// matching a `_proxy` suffix — verified: with only HtTp_PrOxY set it returns
// {'http': ...} — and `.py` is a first-class script interpreter. curl honours
// ALL_PROXY for protocols the specific vars miss. An exact-case blocklist of the
// six canonical names lets both through.
func TestRunScript_StepEnvCannotOverrideProxy(t *testing.T) {
	hostile := map[string]string{
		"HTTP_PROXY":  "",                           // blank it outright
		"https_proxy": "http://evil.example:8080",   // lower-case spelling
		"HtTp_PrOxY":  "http://evil.example:8080",   // Python lowercases the name
		"ALL_PROXY":   "socks5://evil.example:1080", // catch-all curl honours
		"NO_PROXY":    "*",                          // exempt everything from the proxy
	}
	c := &scriptCovContainer{}
	r := newScriptTestRunner(c)
	if _, err := r.RunScript(context.Background(), ScriptRunRequest{
		AuthorCrewID: "crew_1", Interpreter: "python3", Path: "/crew/shared/s.py",
		Env: hostile,
	}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	for _, kv := range c.lastExecCfg.Env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		k, v := kv[:eq], kv[eq+1:]
		if hostile[k] == v && !isPlatformProxyValue(k, v) {
			t.Errorf("step env survived: %q=%q — a script step could redirect or disable its own egress fence (#1473)", k, v)
		}
	}

	// And the platform values must still be the ones that landed.
	got := map[string]string{}
	for _, kv := range c.lastExecCfg.Env {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			got[kv[:eq]] = kv[eq+1:]
		}
	}
	if got["HTTP_PROXY"] != "http://127.0.0.1:9119" {
		t.Errorf("HTTP_PROXY = %q, want the sidecar proxy after a hostile step env", got["HTTP_PROXY"])
	}
	if got["NO_PROXY"] == "*" {
		t.Error(`NO_PROXY = "*" — the step exempted every host from the proxy`)
	}
}

// isPlatformProxyValue reports whether a key/value pair is one the platform
// itself sets, so the hostile-env check doesn't flag a collision where the
// step merely declared the same value we were going to set anyway.
func isPlatformProxyValue(k, v string) bool {
	for _, kv := range orchestrator.SidecarProxyEnv() {
		if kv == k+"="+v {
			return true
		}
	}
	return false
}
