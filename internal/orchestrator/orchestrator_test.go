package orchestrator

import (
	"context"
	"encoding/json"
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
	execResult    *provider.ExecResult
	execErr       error
	inspectResult struct {
		running  bool
		exitCode int
	}
	inspectErr error
}

func (m *mockContainer) EnsureTeamRuntime(_ context.Context, _ provider.TeamConfig) (string, error) {
	return "container-123", nil
}
func (m *mockContainer) StopTeamRuntime(_ context.Context, _ string) error    { return nil }
func (m *mockContainer) RemoveTeamRuntime(_ context.Context, _ string) error  { return nil }
func (m *mockContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (m *mockContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	if m.execErr != nil {
		return nil, m.execErr
	}
	return m.execResult, nil
}
func (m *mockContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return m.inspectResult.running, m.inspectResult.exitCode, m.inspectErr
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
		SessionID:   "s1",
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
	r, w := io.Pipe()
	go func() {
		w.Write([]byte("hello output\n"))
		w.Close()
	}()

	mc := &mockContainer{
		execResult: &provider.ExecResult{ExecID: "exec-1", Reader: r},
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
		SessionID:   "s1",
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
		execResult: &provider.ExecResult{ExecID: "exec-1", Reader: r},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 1},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		SessionID:   "s1",
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
