//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
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
// restorable execution, so probing it fails. That used to be reported as an
// identity error and left the run pending forever. Removal of the whole
// container is authoritative proof that the run is gone, exactly as it already
// is on the tmux path; any other inspect failure is still no proof.
//
// The fixture holds no spool on purpose: the shared retained fixture skips on
// the workload UIDs ordinary CI runs as, and this contract needs only a host
// run whose probe fails, whichever step of the probe fails.
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
			root := t.TempDir()
			state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = state.Close() })
			run := RunState{ID: "removed-container", Status: "running", ContainerID: "container", ExecID: "exec", AgentSlug: "agent", Output: &RunOutputState{
				Version: 2, Directory: filepath.Join(root, "removed-container", "output"), Deadline: time.Now().Add(time.Hour), Adapter: "CLAUDE_CODE",
			}}
			raw, _ := json.Marshal(run)
			if err = state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			o := &Orchestrator{state: state, container: &removedContainerStatus{state: tc.state, err: tc.status}}
			o.SetHostOutputRoot(root)
			stopped, err := o.StopRunAt(t.Context(), RunLocation{ContainerID: run.ContainerID, AgentSlug: "agent", RunID: run.ID})
			if stopped != tc.wantStop || (err != nil) != tc.wantErr {
				t.Fatalf("stopped=%v err=%v, want stopped=%v err=%v", stopped, err, tc.wantStop, tc.wantErr)
			}
		})
	}
}
