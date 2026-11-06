//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type removedContainerStatus struct {
	provider.ContainerProvider
	state string
	err   error
}

func (c *removedContainerStatus) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &provider.ContainerStatus{State: c.state}, nil
}

// A protected run whose container was removed (agent deletion) has no
// restorable execution: the provider's ExecInspect fails, which used to be
// reported as an identity error and left the run pending forever. Removal of
// the whole container is authoritative proof that the run is gone, exactly as
// it already is on the tmux path; any other inspect failure is still no proof.
func TestStopRunAtHostRunRemovedContainerIsStopped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		status   error
		wantStop bool
		wantErr  bool
	}{
		{"removed container", "", provider.ErrContainerNotFound, true, false},
		{"stopped container", "stopped", nil, true, false},
		{"dead container", "error", nil, true, false},
		{"unknown state is no proof", "paused", nil, false, true},
		{"inspect unavailable", "", errors.New("daemon unreachable"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", "", "", 0)
			// No terminal result is retained, so the run is still live as far
			// as its own spool can tell, and the provider cannot restore it.
			if err := os.RemoveAll(run.Output.Directory); err != nil {
				t.Fatal(err)
			}
			run.AgentSlug = "agent"
			raw, _ := json.Marshal(run)
			if err := o.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			o.container = &removedContainerStatus{state: tc.state, err: tc.status}
			stopped, err := o.StopRunAt(t.Context(), RunLocation{ContainerID: run.ContainerID, AgentSlug: "agent", RunID: run.ID})
			if stopped != tc.wantStop || (err != nil) != tc.wantErr {
				t.Fatalf("stopped=%v err=%v, want stopped=%v err=%v", stopped, err, tc.wantStop, tc.wantErr)
			}
		})
	}
}
