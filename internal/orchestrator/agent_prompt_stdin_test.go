package orchestrator

// End-to-end wiring test for the oversized-prompt-via-stdin fix at the
// orchestrator layer. Proves that a >128 KiB user message:
//   1. is delivered to the container exec via ExecConfig.Stdin, and
//   2. never appears as a positional argv element, and
//   3. bypasses the tmux wrapper (a detached tmux session's stdin is not wired
//      to the docker-exec stream, so the prompt would otherwise be lost).
// Also guards that a normal-size message keeps the historic tmux + arg path.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// captureAgentExec returns a mockContainer whose execFn records the ExecConfig
// of the agent CLI exec. The agent exec is either the direct `stdbuf -oL
// claude --print ...` form (stdin path) or the tmux-wrapped `sh -c "... tmux
// new-session ..."` form (historic path).
func captureAgentExec(t *testing.T, capture func(cmd []string, stdin io.Reader)) *mockContainer {
	t.Helper()
	var once sync.Once
	return &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			isAgent := (strings.Contains(joined, "claude") && strings.Contains(joined, "--print")) ||
				strings.Contains(joined, "tmux new-session")
			if isAgent {
				once.Do(func() { capture(cfg.Cmd, cfg.Stdin) })
				return &provider.ExecResult{ExecID: "exec-1", Reader: io.NopCloser(strings.NewReader("ok\n"))}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}
}

func TestRunAgent_LargePrompt_GoesViaStdin_NotArg(t *testing.T) {
	bigMsg := strings.Repeat("D", 200*1024) // 200 KiB — over MAX_ARG_STRLEN

	var gotCmd []string
	var gotStdin string
	var gotStdinSet bool
	mc := captureAgentExec(t, func(cmd []string, stdin io.Reader) {
		gotCmd = cmd
		if stdin != nil {
			gotStdinSet = true
			b, _ := io.ReadAll(stdin)
			gotStdin = string(b)
		}
	})

	o := New(mc, newMemState(), slog.Default())
	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: bigMsg,
		TimeoutSecs: 30,
	}, func(AgentEvent) {})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if !gotStdinSet {
		t.Fatal("ExecConfig.Stdin must be set for an oversized prompt")
	}
	if gotStdin != bigMsg {
		t.Errorf("stdin delivered %d bytes, want %d (must equal the user message)", len(gotStdin), len(bigMsg))
	}
	if argvContains(gotCmd, bigMsg) {
		t.Error("oversized message must NOT be a positional argv element")
	}
	if strings.Contains(strings.Join(gotCmd, " "), "tmux new-session") {
		t.Error("oversized-prompt exec must bypass tmux (its stdin is not wired to the exec stream)")
	}
	// It should be the direct stdbuf-wrapped exec.
	if len(gotCmd) < 3 || gotCmd[0] != "stdbuf" || gotCmd[2] != "claude" {
		t.Errorf("expected direct `stdbuf -oL claude ...` exec, got %v", gotCmd[:min(4, len(gotCmd))])
	}
}

func TestRunAgent_SmallPrompt_KeepsTmuxAndArg(t *testing.T) {
	msg := "hello there"

	var gotCmd []string
	var gotStdinSet bool
	mc := captureAgentExec(t, func(cmd []string, stdin io.Reader) {
		gotCmd = cmd
		gotStdinSet = stdin != nil
	})

	o := New(mc, newMemState(), slog.Default())
	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a2",
		AgentSlug:   "test-agent",
		ChatID:      "s2",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: msg,
		TimeoutSecs: 30,
	}, func(AgentEvent) {})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if gotStdinSet {
		t.Error("small prompt must NOT set ExecConfig.Stdin (keeps historic arg path)")
	}
	if !strings.Contains(strings.Join(gotCmd, " "), "tmux new-session") {
		t.Errorf("small prompt must keep the tmux wrapper; got %v", gotCmd)
	}
}
