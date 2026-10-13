//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type retainedContainerStatus struct {
	provider.ContainerProvider
	mu    sync.Mutex
	state string
}

type hostQualificationProvider struct {
	provider.ContainerProvider
	qualified bool
	err       error
	called    string
}

func (p *hostQualificationProvider) QualifyHostCapture(_ context.Context, id string) (bool, error) {
	p.called = id
	return p.qualified, p.err
}

func TestHostCaptureQualificationFallsBackOnlyWhenUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name      string
		qualified bool
		err       error
	}{
		{name: "qualified", qualified: true},
		{name: "unsupported"},
		{name: "inspection-error", err: errors.New("daemon unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &hostQualificationProvider{qualified: tc.qualified, err: tc.err}
			o := &Orchestrator{container: p}
			req := AgentRunRequest{HostOutputDir: "/trusted/output", ContainerID: "exact-container"}
			err := o.qualifyHostOutput(t.Context(), &req)
			if !errors.Is(err, tc.err) || p.called != req.ContainerID {
				t.Fatalf("qualification error=%v called=%s", err, p.called)
			}
			if (req.HostOutputDir != "") != (tc.qualified || tc.err != nil) {
				t.Fatalf("unexpected fallback after qualification: %+v", req)
			}
		})
	}
	// Providers without a qualifying trusted helper continue through their
	// existing runtime path rather than activating an unverified transport.
	o := &Orchestrator{container: &retainedContainerStatus{}}
	req := AgentRunRequest{HostOutputDir: "/trusted/output"}
	if err := o.qualifyHostOutput(t.Context(), &req); err != nil || req.HostOutputDir != "" {
		t.Fatalf("missing qualifier did not fall back: %v %+v", err, req)
	}
}

func (c *retainedContainerStatus) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &provider.ContainerStatus{State: c.state}, nil
}
func (c *retainedContainerStatus) set(state string) { c.mu.Lock(); c.state = state; c.mu.Unlock() }

func TestHostForcedLeaderExitRetainsOccupancyUntilWholeContainerAbsent(t *testing.T) {
	o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", "", "", 137, "execution_cancelled")
	container := &retainedContainerStatus{state: "running"}
	o.container = container
	if alive, err := o.probeHostRun(t.Context(), run, false); err != nil || !alive {
		t.Fatalf("forced leader occupancy=%v %v", alive, err)
	}
	if stopped, err := o.probeHostRun(t.Context(), run, true); err == nil || stopped {
		t.Fatalf("forced leader falsely absent=%v %v", stopped, err)
	}
	container.set("stopped")
	if alive, err := o.probeHostRun(t.Context(), run, false); err != nil || alive {
		t.Fatalf("whole container absence=%v %v", alive, err)
	}
}

func TestHostDetachedHoldDoesNotRestopOrOverwriteTerminalOutcome(t *testing.T) {
	o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", "", "", 137, "execution_cancelled")
	container := &retainedContainerStatus{state: "running"}
	o.container = container
	o.logger = covQuietLogger()
	o.detached = map[string]*detachedHold{}
	run.Status = "error"
	raw, _ := json.Marshal(run)
	if err := o.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	h := &detachedHold{runID: run.ID, containerID: run.ContainerID, req: AgentRunRequest{RunID: run.ID, HostOutputDir: o.hostOutputRoot}, release: func() { close(released) }}
	if err := o.registerDetachedHold(h, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// More than ten ticks used to trigger the legacy automatic restop. This
	// mock intentionally has no Exec implementation: invoking it would panic.
	select {
	case <-released:
		t.Fatal("forced leader released capacity")
	case <-time.After(30 * time.Millisecond):
	}
	container.set("stopped")
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("authoritative container absence retained capacity")
	}
	got, err := o.state.Get(t.Context(), "agent_runs", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var current RunState
	if err := json.Unmarshal(got, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != "error" {
		t.Fatalf("hold overwrote terminal outcome: %s", current.Status)
	}
}
