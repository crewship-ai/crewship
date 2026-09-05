package orchestrator

// Coverage for PRD-ISSUES-AND-ROUTINES-2026 work package B7 ("Hard
// termination (Tier 2)", #2356): AgentRunRequest.OnExecStarted must fire
// exactly once per run, carrying the ExecID of the heavy agent CLI exec —
// never a preflight/setup exec — so a caller (assignments_run.go) can
// persist "which exec is this run's live process" onto its own row before
// any output has streamed.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// execStartedProbeContainer answers every exec instantly (no blocking, no
// concurrency — this test only cares about which ExecID reaches
// OnExecStarted). The heavy agent CLI exec (tmux new-session ... agent-<slug>,
// same signature run_concurrency_test.go's probe uses) gets a distinctive
// ExecID so it is unmistakable from any setup exec's.
type execStartedProbeContainer struct{}

func (execStartedProbeContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")
	if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-") {
		return &provider.ExecResult{ExecID: "agent-heavy-exec-42", Reader: io.NopCloser(strings.NewReader("done\n"))}, nil
	}
	return &provider.ExecResult{ExecID: "setup-exec", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (execStartedProbeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (execStartedProbeContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-x", nil
}
func (execStartedProbeContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (execStartedProbeContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (execStartedProbeContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (execStartedProbeContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (execStartedProbeContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (execStartedProbeContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

func TestRunAgent_OnExecStarted_FiresOnceWithHeavyExecID(t *testing.T) {
	o := New(execStartedProbeContainer{}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	var mu sync.Mutex
	var got []string
	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "agent-1",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "go",
		TimeoutSecs: 30,
		OnExecStarted: func(execID string) {
			mu.Lock()
			got = append(got, execID)
			mu.Unlock()
		},
	}, nil)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("OnExecStarted called %d times, want exactly 1 (only the heavy agent exec, never a setup exec): %v", len(got), got)
	}
	if got[0] != "agent-heavy-exec-42" {
		t.Errorf("OnExecStarted execID = %q, want the heavy agent exec's id", got[0])
	}
}

// TestRunAgent_NilOnExecStarted_DoesNotPanic pins the "nil is the default
// and a legitimate choice" contract in AgentRunRequest.OnExecStarted's doc —
// every dispatch path that predates B7 (or never sets it) must keep working
// exactly as before.
func TestRunAgent_NilOnExecStarted_DoesNotPanic(t *testing.T) {
	o := New(execStartedProbeContainer{}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a2",
		AgentSlug:   "agent-2",
		ChatID:      "s2",
		ContainerID: "c2",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "go",
		TimeoutSecs: 30,
	}, nil)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
}
