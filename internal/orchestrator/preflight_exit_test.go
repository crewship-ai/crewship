package orchestrator

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// runOrBatch drained the exec's output and returned nil without ever asking
// what it exited with. Every preflight write therefore reported success
// whatever happened — which is how "MCP config injected" was logged for a run
// whose .mcp.json was never created, and claude-code then failed with "MCP
// config file not found" (#1779).
//
// A write that did not happen has to be an error at the point it did not
// happen, not three layers later in someone else's error message.
type exitCodeContainer struct {
	provider.ContainerProvider
	exitCode int
	output   string
}

func (c *exitCodeContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "e1", Reader: io.NopCloser(strings.NewReader(c.output))}, nil
}

func (c *exitCodeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, c.exitCode, nil
}

func TestRunOrBatch_FailsOnNonZeroExit(t *testing.T) {
	ctr := &exitCodeContainer{exitCode: 1, output: "base64: invalid input"}

	err := runOrBatch(context.Background(), ctr, "mcp_config", provider.ExecConfig{
		Cmd: []string{"sh", "-c", "echo x | base64 -d > /crew/agents/a/.mcp.json"},
	})
	if err == nil {
		t.Fatal("a preflight step that exited non-zero must fail, not report success")
	}
	if !strings.Contains(err.Error(), "mcp_config") {
		t.Errorf("error should name the step, got %q", err)
	}
	if !strings.Contains(err.Error(), "base64: invalid input") {
		t.Errorf("error should carry the output that explains it, got %q", err)
	}
}

func TestRunOrBatch_PassesOnZeroExit(t *testing.T) {
	ctr := &exitCodeContainer{exitCode: 0}

	if err := runOrBatch(context.Background(), ctr, "mcp_config", provider.ExecConfig{
		Cmd: []string{"sh", "-c", "true"},
	}); err != nil {
		t.Fatalf("a clean step must succeed: %v", err)
	}
}
