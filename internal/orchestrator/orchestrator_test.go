package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// in-memory state mock
type memState struct {
	data map[string]map[string][]byte
}

func newMemState() *memState {
	return &memState{data: make(map[string]map[string][]byte)}
}
func (m *memState) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if b, ok := m.data[bucket]; ok {
		return b[key], nil
	}
	return nil, nil
}
func (m *memState) Set(_ context.Context, bucket, key string, value []byte) error {
	if m.data[bucket] == nil {
		m.data[bucket] = make(map[string][]byte)
	}
	m.data[bucket][key] = value
	return nil
}
func (m *memState) Delete(_ context.Context, bucket, key string) error {
	if b, ok := m.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}
func (m *memState) List(_ context.Context, bucket string) (map[string][]byte, error) {
	return m.data[bucket], nil
}
func (m *memState) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}
func (m *memState) Close() error { return nil }

// mock container provider
type mockContainer struct {
	execResults   []*provider.ExecResult
	execErr       error
	execCallIdx   int
	execFn        func(cfg provider.ExecConfig) (*provider.ExecResult, error) // callback-based mock
	inspectResult struct {
		running  bool
		exitCode int
	}
	inspectErr error
}

func (m *mockContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-123", nil
}
func (m *mockContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *mockContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *mockContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (m *mockContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	// Callback-based mock takes priority
	if m.execFn != nil {
		return m.execFn(cfg)
	}
	if m.execErr != nil {
		return nil, m.execErr
	}
	// AGENT CLI CALL: when cmd[0..N] contains a known CLI binary
	// (claude/codex/...) the call is the actual agent exec. Always return
	// the LAST entry in execResults — that's where tests put the real
	// stream reader. This decouples tests from the exact number of setup
	// calls (mkdir, manifest, MCP, canonical memory writes, etc.) so
	// adding new setup steps doesn't shift the agent exec out of bounds.
	cliBinaries := map[string]bool{
		"claude": true, "codex": true, "gemini": true,
		"opencode": true, "cursor-agent": true, "droid": true,
	}
	for i := 0; i < len(cfg.Cmd) && i < 6; i++ {
		if cliBinaries[cfg.Cmd[i]] && len(m.execResults) > 0 {
			return m.execResults[len(m.execResults)-1], nil
		}
	}
	// SETUP CALLS (sh -c "..." for file writes etc.) — return noop. Tests
	// that need to assert specific setup-call results should use execFn
	// callback mode instead of the positional execResults FIFO.
	idx := m.execCallIdx
	m.execCallIdx++
	if idx < len(m.execResults)-1 {
		return m.execResults[idx], nil
	}
	return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *mockContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return m.inspectResult.running, m.inspectResult.exitCode, m.inspectErr
}
func (m *mockContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *mockContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}
func (m *mockContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

func TestNew(t *testing.T) {
	o := New(nil, nil, slog.Default())
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	if !o.accepting {
		t.Error("expected accepting=true on init")
	}
}

func TestStopAccepting(t *testing.T) {
	o := New(nil, nil, slog.Default())
	o.StopAccepting()
	if o.accepting {
		t.Error("expected accepting=false after StopAccepting")
	}
}

func TestRunAgentNotAccepting(t *testing.T) {
	o := New(nil, newMemState(), slog.Default())
	o.StopAccepting()

	err := o.RunAgent(context.Background(), AgentRunRequest{}, nil)
	if err == nil {
		t.Fatal("expected error when not accepting")
	}
	if !strings.Contains(err.Error(), "not accepting") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunAgentExecError(t *testing.T) {
	mc := &mockContainer{
		execErr: io.ErrClosedPipe,
	}
	o := New(mc, newMemState(), slog.Default())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "test",
		TimeoutSecs: 5,
	}, nil)

	if err == nil {
		t.Fatal("expected error from exec")
	}
	if !strings.Contains(err.Error(), "exec agent") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunAgentSuccess(t *testing.T) {
	// SetupSystemPrompt now writes 5 canonical memory files, plus various
	// other setup execs (mkdir, manifest, claude config, tmux check). Use
	// execFn callback to deterministically detect the agent exec by looking
	// for "--print" arg (Claude Code's signature flag), regardless of how
	// many setup execs come before.
	// orchestrator_run.go wraps the agent CLI in tmux: the actual
	// `claude --print ...` ends up inside /tmp/agent-<slug>.sh and the
	// outer cfg.Cmd is `[sh -c "tmux new-session ... 'sh /tmp/...sh' ..."]`.
	// Detect the agent exec by its tmux session-name signature.
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-test-agent") {
				return &provider.ExecResult{ExecID: "exec-1", Reader: io.NopCloser(strings.NewReader("hello output\n"))}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())

	var events []AgentEvent
	handler := func(e AgentEvent) { events = append(events, e) }

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "test",
		TimeoutSecs: 30,
	}, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) == 0 {
		t.Error("expected at least one event")
	}

	// Check state was persisted
	data, _ := state.Get(context.Background(), "agent_runs", "s1")
	if data == nil {
		t.Fatal("expected run state to be persisted")
	}
	var run RunState
	json.Unmarshal(data, &run)
	if run.Status != "completed" {
		t.Errorf("expected completed status, got %q", run.Status)
	}
}

func TestRunAgentExitCodeError(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		w.Write([]byte("error output\n"))
		w.Close()
	}()

	mc := &mockContainer{
		execResults: []*provider.ExecResult{
			{ExecID: "tmux-check", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "mkdir-1", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "manifest-1", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "exec-1", Reader: r},
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 1},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		TimeoutSecs: 5,
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := state.Get(context.Background(), "agent_runs", "s1")
	var run RunState
	json.Unmarshal(data, &run)
	if run.Status != "error" {
		t.Errorf("expected error status for non-zero exit, got %q", run.Status)
	}
}

func TestRunAgentInvalidSlug(t *testing.T) {
	mc := &mockContainer{}
	o := New(mc, newMemState(), slog.Default())

	for _, slug := range []string{"", "../escape", "a/b", "..", "bad slug"} {
		err := o.RunAgent(context.Background(), AgentRunRequest{
			AgentID:     "a1",
			AgentSlug:   slug,
			ChatID:      "s1",
			ContainerID: "c1",
			TimeoutSecs: 5,
		}, nil)
		if err == nil {
			t.Errorf("expected error for invalid slug %q", slug)
		}
		if !strings.Contains(err.Error(), "invalid agent slug") {
			t.Errorf("expected 'invalid agent slug' error for %q, got: %v", slug, err)
		}
	}
}

func TestSelectCredentialEmpty(t *testing.T) {
	o := New(nil, nil, slog.Default())
	c := o.selectCredential(nil)
	if c != nil {
		t.Error("expected nil for empty creds")
	}
}

func TestSelectCredentialSingle(t *testing.T) {
	o := New(nil, nil, slog.Default())
	creds := []Credential{{ID: "c1", EnvVarName: "KEY", PlainValue: "val"}}
	c := o.selectCredential(creds)
	if c == nil || c.ID != "c1" {
		t.Error("expected cred c1")
	}
}

func TestSelectCredentialSkipsCooldown(t *testing.T) {
	o := New(nil, nil, slog.Default())
	o.cooldown.MarkCooldown("c1", 1*60*1e9) // 1 min
	creds := []Credential{
		{ID: "c1", EnvVarName: "KEY", PlainValue: "v1", Priority: 0},
		{ID: "c2", EnvVarName: "KEY", PlainValue: "v2", Priority: 1},
	}
	c := o.selectCredential(creds)
	if c == nil || c.ID != "c2" {
		t.Errorf("expected c2 (c1 in cooldown), got %v", c)
	}
}

func TestRecoverFromCrash(t *testing.T) {
	mc := &mockContainer{
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}
	state := newMemState()

	run := RunState{ID: "r1", AgentID: "a1", Status: "running", ExecID: "e1"}
	data, _ := json.Marshal(run)
	state.Set(context.Background(), "agent_runs", "r1", data)

	completedRun := RunState{ID: "r2", AgentID: "a2", Status: "completed"}
	data2, _ := json.Marshal(completedRun)
	state.Set(context.Background(), "agent_runs", "r2", data2)

	o := New(mc, state, slog.Default())
	err := o.RecoverFromCrash(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d, _ := state.Get(context.Background(), "agent_runs", "r1")
	var recovered RunState
	json.Unmarshal(d, &recovered)
	if recovered.Status != "completed" {
		t.Errorf("expected recovered run to be completed, got %q", recovered.Status)
	}

	d2, _ := state.Get(context.Background(), "agent_runs", "r2")
	var unchanged RunState
	json.Unmarshal(d2, &unchanged)
	if unchanged.Status != "completed" {
		t.Errorf("expected already completed run unchanged, got %q", unchanged.Status)
	}
}

func TestRecoverFromCrashNoExecID(t *testing.T) {
	state := newMemState()
	run := RunState{ID: "r1", AgentID: "a1", Status: "running", ExecID: ""}
	data, _ := json.Marshal(run)
	state.Set(context.Background(), "agent_runs", "r1", data)

	o := New(nil, state, slog.Default())
	_ = o.RecoverFromCrash(context.Background())

	d, _ := state.Get(context.Background(), "agent_runs", "r1")
	var recovered RunState
	json.Unmarshal(d, &recovered)
	if recovered.Status != "error" {
		t.Errorf("expected error for run without exec ID, got %q", recovered.Status)
	}
}

// A transient ExecInspect error (e.g. Docker daemon briefly unreachable on
// startup) must NOT cause an in-flight run to be marked completed. The next
// recovery pass — or the run's own exec — will reconcile state. Marking it
// completed on a transient error silently terminates live work.
func TestRecoverFromCrashTransientInspectError(t *testing.T) {
	mc := &mockContainer{
		// running=true so a "completed" outcome can only come from the bug
		// (collapsing err with !running).
		inspectResult: struct {
			running  bool
			exitCode int
		}{true, 0},
		inspectErr: errors.New("docker daemon unavailable"),
	}
	state := newMemState()

	run := RunState{ID: "r1", AgentID: "a1", Status: "running", ExecID: "e1"}
	data, _ := json.Marshal(run)
	state.Set(context.Background(), "agent_runs", "r1", data)

	o := New(mc, state, slog.Default())
	if err := o.RecoverFromCrash(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d, _ := state.Get(context.Background(), "agent_runs", "r1")
	var recovered RunState
	if err := json.Unmarshal(d, &recovered); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if recovered.Status == "completed" {
		t.Errorf("transient inspect error must not mark live run as completed")
	}
	if recovered.Status != "running" {
		t.Errorf("expected status to stay %q, got %q", "running", recovered.Status)
	}
}

func TestRunAgentScrubsCredentials(t *testing.T) {
	// Detect agent CLI exec by tmux-session signature; see TestRunAgentSuccess
	// for the rationale (canonical-memory writes shifted indexing).
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-test-agent") {
				return &provider.ExecResult{
					ExecID: "exec-1",
					Reader: io.NopCloser(strings.NewReader("Found key: sk-ant-api03-secretkey1234567890\n")),
				}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())

	var events []AgentEvent
	handler := func(e AgentEvent) { events = append(events, e) }

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CODEX_CLI", // non-JSON output for simplicity
		UserMessage: "test",
		TimeoutSecs: 30,
	}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	foundRedacted := false
	for _, e := range events {
		if strings.Contains(e.Content, "sk-ant-") && e.Type != "system" {
			t.Errorf("credential leaked in event content: %q", e.Content)
		}
		if strings.Contains(e.Content, "[REDACTED:anthropic_key]") {
			foundRedacted = true
		}
	}
	if !foundRedacted {
		t.Error("expected at least one event with [REDACTED:anthropic_key] marker")
	}
}

func TestRunAgentWithSidecar(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		w.Write([]byte("agent output via sidecar\n"))
		w.Close()
	}()

	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-test-agent") {
				return &provider.ExecResult{ExecID: "exec-1", Reader: r}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())
	o.SetSidecarEnabled(true)

	var events []AgentEvent
	handler := func(e AgentEvent) { events = append(events, e) }

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CODEX_CLI",
		UserMessage: "test",
		TimeoutSecs: 30,
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-real-secret", Priority: 1},
		},
	}, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	// Verify the exec was called with proxy env vars (not real credentials)
	// The mock doesn't capture env vars directly, but we verify the flow works
	// and that BuildEnvVarsSidecar is used (tested separately in failover_test.go)
}

func TestRunAgentCancelledContext(t *testing.T) {
	// When the context is cancelled (user pressed stop), RunAgent should:
	// 1. Return an error containing "run cancelled"
	// 2. Update run state to "cancelled"
	r, w := io.Pipe()

	mc := &mockContainer{
		execResults: []*provider.ExecResult{
			{ExecID: "tmux-check", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "mkdir-1", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "config-1", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "exec-1", Reader: r},
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	// Close the writer when the context is cancelled to unblock readers
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	// Cancel immediately to simulate user pressing stop
	go func() {
		// Small delay to let RunAgent start the exec
		cancel()
	}()

	err := o.RunAgent(ctx, AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "test",
		TimeoutSecs: 30,
	}, nil)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("expected 'cancelled' in error, got: %v", err)
	}

	// Verify run state is "cancelled"
	data, _ := state.Get(context.Background(), "agent_runs", "s1")
	if data != nil {
		var run RunState
		json.Unmarshal(data, &run)
		if run.Status != "cancelled" {
			t.Errorf("expected cancelled status, got %q", run.Status)
		}
	}
}

// TestInjectMCPCredentialEnvVarsRespectsLiteralValues verifies that an
// MCP server config carrying a *literal* env value (not a ${VAR}
// reference) is treated as the caller's authoritative choice. A
// matching credential by name must NOT silently shadow the literal —
// the previous behavior added the env key to the "needed refs" set,
// which made any same-named credential overwrite the literal at exec
// time.
func TestInjectMCPCredentialEnvVarsRespectsLiteralValues(t *testing.T) {
	req := AgentRunRequest{
		MCPServers: []MCPServerConfig{{
			ID:        "github",
			Transport: "stdio",
			Env: map[string]string{
				"GH_TOKEN": "literal-from-yaml",
				"GH_HOST":  "${GH_HOST}", // genuine reference — should resolve
			},
		}},
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "credential-secret", Priority: 0},
			{ID: "c2", EnvVarName: "GH_HOST", PlainValue: "github.example.com", Priority: 0},
		},
	}

	got := injectMCPCredentialEnvVars(req, nil)

	var sawLiteralOverride bool
	var sawHostInjected bool
	for _, e := range got {
		if e == "GH_TOKEN=credential-secret" {
			sawLiteralOverride = true
		}
		if e == "GH_HOST=github.example.com" {
			sawHostInjected = true
		}
	}
	if sawLiteralOverride {
		t.Errorf("credential silently overrode literal Env value: env=%v", got)
	}
	if !sawHostInjected {
		t.Errorf("explicit ${GH_HOST} reference should still resolve from credentials: env=%v", got)
	}
}

// TestInjectMCP_HTTPHeaderBearerToken pins the production-blocking gap from
// the third validation wave: HTTP MCP servers like Linear use Authorization:
// Bearer ${TOKEN} headers, which the pre-fix collectMCPEnvRefs did not scan
// — the bearer token was never injected and every HTTP MCP server hit
// upstream with literal "${TOKEN}" as the credential, returning 401.
func TestInjectMCP_HTTPHeaderBearerToken(t *testing.T) {
	crewJSON := `{"mcpServers":{"linear":{"type":"http","url":"https://mcp.linear.app/sse","headers":{"Authorization":"Bearer ${LINEAR_TOKEN}"}}}}`
	req := AgentRunRequest{
		CrewMCPConfigJSON: crewJSON,
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "LINEAR_TOKEN", PlainValue: "lin_real_secret"},
		},
	}
	got := injectMCPCredentialEnvVars(req, nil)

	found := false
	for _, e := range got {
		if e == "LINEAR_TOKEN=lin_real_secret" {
			found = true
		}
	}
	if !found {
		t.Fatalf("LINEAR_TOKEN must be injected — Authorization header reference was missed by the pre-fix prefix-only scanner. env=%v", got)
	}
}

// TestInjectMCP_CursorEnvSyntax — Cursor uses ${env:VAR} (not ${VAR}). The
// scanner must accept that form so credentials referenced in Cursor MCP
// configs get injected.
func TestInjectMCP_CursorEnvSyntax(t *testing.T) {
	cfg := `{"mcpServers":{"linear":{"type":"http","url":"https://mcp.linear.app/sse","headers":{"Authorization":"Bearer ${env:LINEAR_TOKEN}"}}}}`
	refs := collectMCPEnvRefs(cfg)
	if !refs["LINEAR_TOKEN"] {
		t.Errorf("Cursor ${env:VAR} syntax not picked up — refs=%v", refs)
	}
}

// TestInjectMCP_BareDollarVar — bare $VAR form (no curlies) must also be
// scanned, otherwise terse env values break.
func TestInjectMCP_BareDollarVar(t *testing.T) {
	cfg := `{"mcpServers":{"x":{"command":"npx","env":{"FOO":"$BAR"}}}}`
	refs := collectMCPEnvRefs(cfg)
	if !refs["BAR"] {
		t.Errorf("bare $VAR not picked up — refs=%v", refs)
	}
}

// TestInjectMCP_MultipleRefsInOneValue — value like "Bearer ${A} for ${B}"
// must extract BOTH names.
func TestInjectMCP_MultipleRefsInOneValue(t *testing.T) {
	cfg := `{"mcpServers":{"x":{"type":"http","url":"https://example.com","headers":{"Authorization":"Bearer ${A} for ${B}"}}}}`
	refs := collectMCPEnvRefs(cfg)
	if !refs["A"] || !refs["B"] {
		t.Errorf("multiple env refs in one value not picked up — refs=%v", refs)
	}
}

// TestInjectMCP_URLEnvRef — ${VAR} embedded in the url field (rare but seen).
func TestInjectMCP_URLEnvRef(t *testing.T) {
	cfg := `{"mcpServers":{"x":{"type":"http","url":"https://${TENANT}.example.com/mcp"}}}`
	refs := collectMCPEnvRefs(cfg)
	if !refs["TENANT"] {
		t.Errorf("env ref in url field not scanned — refs=%v", refs)
	}
}

// TestExtractEnvRefs_NoFalsePositives — literal strings without env refs must
// return nothing. Silent injection of literal strings would be a security
// problem (we'd add credentials to env when none were requested).
func TestExtractEnvRefs_NoFalsePositives(t *testing.T) {
	cases := []string{
		"literal-token-value",
		"sk-ant-api03-12345",
		"$",                   // dangling dollar with nothing after
		"text $1 placeholder", // shell positional, NOT an env var
	}
	for _, c := range cases {
		if refs := extractEnvRefs(c); len(refs) != 0 {
			t.Errorf("false positive on %q: %v", c, refs)
		}
	}
}
