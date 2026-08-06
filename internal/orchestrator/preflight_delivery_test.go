package orchestrator

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The batch could not tell "every step succeeded" from "the script never ran".
//
// It reads failures out of the script's OWN output and separately checks for a
// non-zero exit. A script that was never delivered produces neither: empty
// output, exit 0 — which reads as total success. That is exactly what happened
// on Apple Containers, where exec dropped stdin: six preflight steps reported
// clean and none of them had executed, and the run only fell over later inside
// the agent (#1779).
//
// Both existing guards ask HOW it failed. Neither can ask WHETHER it ran, so
// the script now has to prove it reached the end.
type scriptedContainer struct {
	provider.ContainerProvider
	output   string
	exitCode int
	sawStdin bool
}

func (c *scriptedContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if cfg.Stdin != nil {
		b, _ := io.ReadAll(cfg.Stdin)
		c.sawStdin = len(b) > 0
	}
	return &provider.ExecResult{ExecID: "e1", Reader: io.NopCloser(strings.NewReader(c.output))}, nil
}

func (c *scriptedContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, c.exitCode, nil
}

func newBatchFor(ctr provider.ContainerProvider) *preflightBatch {
	b := newPreflightBatch(ctr, "cid", "1001:1001", nil)
	b.add("mcp-config", "", "echo hi")
	b.add("agent-dirs", "", "mkdir -p /crew/agents/x")
	return b
}

// A script that produced nothing and exited 0 did not run. Treat every queued
// step as failed rather than as done.
func TestPreflightFlush_UndeliveredScriptFailsEveryStep(t *testing.T) {
	ctr := &scriptedContainer{output: "", exitCode: 0}
	b := newBatchFor(ctr)

	err := b.Flush(context.Background())
	if err == nil {
		t.Fatal("a script that never ran must not report success")
	}
	for _, step := range []string{"mcp-config", "agent-dirs"} {
		if !b.stepFailed(step) {
			t.Errorf("step %q must be marked failed when the script was not delivered", step)
		}
	}
}

// The completion marker is the positive assertion: present means the shell
// reached the end of the script, whatever the steps did.
func TestPreflightFlush_CompletedScriptWithNoFailuresPasses(t *testing.T) {
	ctr := &scriptedContainer{output: preflightStepMarker + "mcp-config\n" + preflightDoneMarker + "\n", exitCode: 0}
	b := newBatchFor(ctr)

	if err := b.Flush(context.Background()); err != nil {
		t.Fatalf("a completed script with no failures must pass: %v", err)
	}
	for _, step := range []string{"mcp-config", "agent-dirs"} {
		if b.stepFailed(step) {
			t.Errorf("step %q must not be marked failed", step)
		}
	}
	if !ctr.sawStdin {
		t.Error("the script must reach the container over stdin")
	}
}

// A script that ran and reported a real failure keeps behaving as before —
// the new check must not mask the old signal.
func TestPreflightFlush_ReportedFailureStillMarksThatStep(t *testing.T) {
	ctr := &scriptedContainer{
		output:   preflightFailMarker + "mcp-config \n" + preflightDoneMarker + "\n",
		exitCode: 1,
	}
	b := newBatchFor(ctr)

	_ = b.Flush(context.Background())
	if !b.stepFailed("mcp-config") {
		t.Error("a step the script reported as failed must stay failed")
	}
	if b.stepFailed("agent-dirs") {
		t.Error("a step the script did not report must not be collateral")
	}
}
