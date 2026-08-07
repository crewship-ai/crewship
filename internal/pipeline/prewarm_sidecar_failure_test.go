package pipeline

// A container that came up must be registered, even when the start reported an
// error afterwards.
//
// #1662 closed the untracked-container leak: a prewarm or script step that woke
// a crew container and registered nothing left it running with no TTL for the
// idle reaper and no stats — until crewshipd restarted. Sidecars reopened it
// from a new direction: the runtime container is up BEFORE the sidecars are
// started, so an ErrSidecarStart return means "container running, registered
// nowhere". prewarm.go swallows that error into a debug line, so it repeats on
// every prewarm for as long as the sidecar image stays broken.
//
// The assertions are on the registrations, because those are what the reaper
// and the stats collector read. Sibling of runner_wake_registration_test.go,
// which pins the same two facts on the success path.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// brokenSidecarProvider starts runtimes fine and fails every sidecar.
type brokenSidecarProvider struct {
	mu      sync.Mutex
	started []string
}

func (p *brokenSidecarProvider) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = append(p.started, cfg.ID)
	return "ctr_" + cfg.ID, nil
}
func (p *brokenSidecarProvider) StopCrewRuntime(context.Context, string) error   { return nil }
func (p *brokenSidecarProvider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (p *brokenSidecarProvider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (p *brokenSidecarProvider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (p *brokenSidecarProvider) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(nil)}, nil
}
func (p *brokenSidecarProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (p *brokenSidecarProvider) CrewContainerName(_, slug string) string { return "crew-" + slug }
func (p *brokenSidecarProvider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (p *brokenSidecarProvider) EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error) {
	return nil, errors.New("pull access denied for redis:7-alpine")
}
func (p *brokenSidecarProvider) StopCrewServices(context.Context, string, string) error { return nil }
func (p *brokenSidecarProvider) RemoveCrewServices(context.Context, string, string) error {
	return nil
}

var _ provider.ContainerProvider = (*brokenSidecarProvider)(nil)
var _ provider.SidecarProvider = (*brokenSidecarProvider)(nil)

func brokenSidecarRunner(t *testing.T, cp provider.ContainerProvider) (*OrchestratorRunner, func(string) (string, string, string)) {
	t.Helper()
	orch := wakeTestOrchestrator()
	var mu sync.Mutex
	var gotContainer, gotCrew, gotWS string
	orch.SetStatsRegisterCallback(func(containerID, crewID, workspaceID string) {
		mu.Lock()
		defer mu.Unlock()
		gotContainer, gotCrew, gotWS = containerID, crewID, workspaceID
	})
	r := &OrchestratorRunner{
		container: cp,
		orch:      orch,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, crewID, _ string) (provider.CrewConfig, error) {
			return provider.CrewConfig{
				ID:       crewID,
				Slug:     "data",
				TTLHours: 3,
				Services: []provider.CrewService{{Name: "redis", Image: "redis:7-alpine"}},
			}, nil
		},
	}
	stats := func(string) (string, string, string) {
		mu.Lock()
		defer mu.Unlock()
		return gotContainer, gotCrew, gotWS
	}
	return r, stats
}

func TestPrewarmRegistersTheContainerEvenWhenSidecarsFail(t *testing.T) {
	cp := &brokenSidecarProvider{}
	r, stats := brokenSidecarRunner(t, cp)

	err := r.PrewarmCrew(context.Background(), "crew_p", "ws_1")
	if err == nil {
		t.Fatal("a failed sidecar must still be reported to the caller")
	}
	if len(cp.started) == 0 {
		t.Fatal("the runtime container was never started")
	}

	act, ok := r.orch.CrewActivity("crew_p")
	if !ok {
		t.Error("the crew container is up and NOTHING registered it: no TTL, so the idle reaper " +
			"never stops it and it runs until crewshipd restarts. That is the #1662 leak, reopened " +
			"through the sidecar error path — and prewarm swallows the error, so it repeats on every " +
			"prewarm while the sidecar stays broken.")
	} else if act.ContainerID != "ctr_crew_p" || act.TTLHours != 3 {
		t.Errorf("tracked activity = %+v, want container ctr_crew_p with the crew's TTL of 3", act)
	}

	if c, crew, ws := stats(""); c != "ctr_crew_p" || crew != "crew_p" || ws != "ws_1" {
		t.Errorf("stats registration = (%q,%q,%q), want (ctr_crew_p, crew_p, ws_1) — the container "+
			"is really running, so the dashboard tile must not stay empty", c, crew, ws)
	}
}
