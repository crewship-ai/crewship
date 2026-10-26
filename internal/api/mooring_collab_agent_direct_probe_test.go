package api

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

// These provider states exercise the real private leaf and generic location
// probe. They are not evidence of actual Docker or private CLI execution.
type collaborationDirectProbeContainer struct {
	independentPrivateDirectProbeProvider
	running    bool
	inspectErr error
	status     *provider.ContainerStatus
	statusErr  error
	inspected  []string
}

func (p *collaborationDirectProbeContainer) ExecInspect(_ context.Context, id string) (bool, int, error) {
	p.inspected = append(p.inspected, id)
	return p.running, 0, p.inspectErr
}
func (p *collaborationDirectProbeContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return p.status, p.statusErr
}

func TestCollaborationAgentRuntimeDirectProbeKeepsOriginalExecAuthority(t *testing.T) {
	for _, tc := range []struct {
		name             string
		container        collaborationDirectProbeContainer
		execID           string
		alive, uncertain bool
	}{
		{"live", collaborationDirectProbeContainer{running: true}, "original-exec", true, false},
		{"terminal", collaborationDirectProbeContainer{}, "original-exec", false, false},
		{"inspect-uncertain", collaborationDirectProbeContainer{inspectErr: errors.New("private diagnostic"), status: &provider.ContainerStatus{State: "running"}}, "original-exec", false, true},
		{"container-absent", collaborationDirectProbeContainer{inspectErr: errors.New("private diagnostic"), statusErr: provider.ErrContainerNotFound}, "original-exec", false, false},
		{"container-stopped", collaborationDirectProbeContainer{inspectErr: errors.New("private diagnostic"), status: &provider.ContainerStatus{State: "stopped"}}, "original-exec", false, false},
		{"container-error", collaborationDirectProbeContainer{inspectErr: errors.New("private diagnostic"), status: &provider.ContainerStatus{State: "error"}}, "original-exec", false, false},
		{"unknown-container", collaborationDirectProbeContainer{inspectErr: errors.New("private diagnostic"), status: &provider.ContainerStatus{}}, "original-exec", false, true},
		{"nil-container-state", collaborationDirectProbeContainer{inspectErr: errors.New("private diagnostic")}, "original-exec", false, true},
		{"missing-requested-id", collaborationDirectProbeContainer{running: true}, "", false, true},
	} {
		for _, method := range []string{"alive", "stop"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				p := tc.container
				owner := orchestrator.New(&p, nil, newTestLogger())
				rt := &collaborationAgentTaskRuntime{WebhookRuntime: NewWebhookRuntime(&WebhookHandler{orch: owner, container: &p})}
				location := orchestrator.RunLocation{ContainerID: "private-container", AgentSlug: "private-agent", RunID: "private-run"}
				rt.launches.Store(location.RunID, &webhookLaunch{requested: true, confirmed: true, nativeExecID: tc.execID, location: &location})
				want := tc.alive
				var got bool
				var err error
				if method == "alive" {
					got, err = rt.Alive(t.Context(), "agent-run:"+location.RunID)
				} else {
					want = !tc.alive && !tc.uncertain
					got, err = rt.Stop(t.Context(), "agent-run:"+location.RunID)
				}
				if got != want || (err != nil) != tc.uncertain {
					t.Fatalf("got=%v err=%v want=%v uncertain=%v", got, err, want, tc.uncertain)
				}
				if err != nil && !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) {
					t.Fatal("raw provider diagnostic escaped")
				}
				if tc.execID != "" && (len(p.inspected) != 1 || p.inspected[0] != tc.execID) {
					t.Fatal("probe did not inspect exact original exec", p.inspected)
				}
				if tc.execID == "" && len(p.inspected) != 0 {
					t.Fatal("missing identity invented exec")
				}
			})
		}
	}
}

type collaborationCaptureProbeContainer struct{ collaborationRuntimeContainer }

func (*collaborationCaptureProbeContainer) ExecInspect(_ context.Context, id string) (bool, int, error) {
	if id != "synthetic-exec" {
		return false, 0, errors.New("wrong original exec")
	}
	return true, 0, nil
}

func TestCollaborationAgentRuntimeCapturesExecBeforeConfirmedPublication(t *testing.T) {
	x, d, h, _, _, a := collaborationRuntimeFixture(t)
	h.container = &collaborationCaptureProbeContainer{}
	var rt *collaborationAgentTaskRuntime
	var err error
	observed := false
	rt, err = newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(ctx context.Context, c collaborationAgentConfirmed) error {
		alive, e := rt.Alive(ctx, "agent-run:"+c.runID)
		if e != nil || !alive || c.execID != "synthetic-exec" {
			t.Fatal("confirmed publication preceded original exec capture", alive, e)
		}
		observed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.Run(t.Context(), a, func() {
		if e := work.NewStore(x.f.rig.db).StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, rt.Locator(a)); e != nil {
			t.Fatal(e)
		}
	})
	if !observed {
		t.Fatal("actual confirmed callback was not observed")
	}
}

func TestCollaborationAgentRuntimeStopBeforeCreationKeepsOriginalGate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	location := orchestrator.RunLocation{ContainerID: "private-container", AgentSlug: "private-agent", RunID: "private-run"}
	launch := &webhookLaunch{cancel: cancel, location: &location}
	rt := &collaborationAgentTaskRuntime{WebhookRuntime: NewWebhookRuntime(&WebhookHandler{})}
	rt.launches.Store(location.RunID, launch)
	stopped, err := rt.Stop(t.Context(), "agent-run:"+location.RunID)
	if err != nil || !stopped || ctx.Err() == nil {
		t.Fatal("pre-creation stop lost its original gate", stopped, err)
	}
	if err = launch.RequestCreation(t.Context()); !errors.Is(err, errWebhookStoppedBeforeAgent) {
		t.Fatal("stopped gate admitted creation", err)
	}
}
