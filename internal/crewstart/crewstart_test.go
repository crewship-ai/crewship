package crewstart

// What "start a crew" has to mean, everywhere.
//
// The live proof for these is in the container runtime — a redis that answers
// PING from inside the crew container, a shell that finds the crew's own
// toolchain. What is pinned here is the contract those depend on: the sidecars
// are asked for, they are asked for AFTER the runtime exists, the crew's
// provisioned image survives a caller that knew nothing about it, and a
// provider with no sidecar capability degrades instead of failing.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// fakeRuntime is a ContainerProvider that records the order of the calls it
// receives — the ordering is load-bearing, not cosmetic: sidecars join the
// crew's bridge network, which does not exist until the runtime is up.
type fakeRuntime struct {
	calls       []string
	runtimeCfg  provider.CrewConfig
	servicesCfg provider.CrewConfig
	ensureErr   error
	servicesErr error
}

func (f *fakeRuntime) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	f.calls = append(f.calls, "runtime")
	f.runtimeCfg = cfg
	if f.ensureErr != nil {
		return "", f.ensureErr
	}
	return "container-" + cfg.ID, nil
}
func (f *fakeRuntime) StopCrewRuntime(context.Context, string) error   { return nil }
func (f *fakeRuntime) RemoveCrewRuntime(context.Context, string) error { return nil }
func (f *fakeRuntime) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (f *fakeRuntime) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *fakeRuntime) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(nil)}, nil
}
func (f *fakeRuntime) ExecInspect(context.Context, string) (bool, int, error) { return false, 0, nil }
func (f *fakeRuntime) CrewContainerName(_, slug string) string                { return "crew-" + slug }
func (f *fakeRuntime) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

// sidecarRuntime adds the optional sidecar capability.
type sidecarRuntime struct{ *fakeRuntime }

func (s *sidecarRuntime) EnsureCrewServices(_ context.Context, cfg provider.CrewConfig) (map[string]string, error) {
	s.calls = append(s.calls, "services")
	s.servicesCfg = cfg
	if s.servicesErr != nil {
		return nil, s.servicesErr
	}
	ids := map[string]string{}
	for _, svc := range cfg.Services {
		ids[svc.Name] = "svc-" + svc.Name
	}
	return ids, nil
}
func (s *sidecarRuntime) StopCrewServices(context.Context, string) error   { return nil }
func (s *sidecarRuntime) RemoveCrewServices(context.Context, string) error { return nil }

var _ provider.ContainerProvider = (*fakeRuntime)(nil)
var _ provider.SidecarProvider = (*sidecarRuntime)(nil)

func redisConfig() provider.CrewConfig {
	return provider.CrewConfig{
		ID:   "crew-1",
		Slug: "data",
		Services: []provider.CrewService{
			{Name: "redis", Image: "redis:7-alpine", Env: map[string]string{"REDIS_PASSWORD": "s3cret"}},
		},
	}
}

func TestStartBringsDeclaredSidecarsUpAfterTheRuntime(t *testing.T) {
	f := &fakeRuntime{}
	ctr := &sidecarRuntime{fakeRuntime: f}

	id, err := New(ctr, nil, nil).Start(context.Background(), redisConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id != "container-crew-1" {
		t.Errorf("container id = %q", id)
	}
	if len(f.calls) != 2 || f.calls[0] != "runtime" || f.calls[1] != "services" {
		t.Fatalf("call order = %v, want [runtime services] — a sidecar started before the runtime "+
			"has no crew network to join", f.calls)
	}
	if len(f.servicesCfg.Services) != 1 || f.servicesCfg.Services[0].Name != "redis" {
		t.Errorf("services asked for = %+v, want the crew's redis", f.servicesCfg.Services)
	}
	if got := f.servicesCfg.Services[0].Env["REDIS_PASSWORD"]; got != "s3cret" {
		t.Errorf("REDIS_PASSWORD = %q, want the resolved value", got)
	}
}

func TestStartWithoutServicesNeverTouchesTheSidecarPath(t *testing.T) {
	f := &fakeRuntime{}
	ctr := &sidecarRuntime{fakeRuntime: f}

	if _, err := New(ctr, nil, nil).Start(context.Background(), provider.CrewConfig{ID: "crew-2"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != "runtime" {
		t.Errorf("calls = %v, want the runtime only — a crew with no services must not pay for "+
			"the sidecar path", f.calls)
	}
}

// A provider without the optional sidecar capability (apple-container today)
// must still start the crew, and must say what it dropped.
func TestStartDegradesOnAProviderWithoutSidecars(t *testing.T) {
	f := &fakeRuntime{}

	var notices []Notice
	id, err := New(f, nil, nil).StartNotify(context.Background(), redisConfig(), func(n Notice) {
		notices = append(notices, n)
	})
	if err != nil {
		t.Fatalf("a provider without sidecars must not fail the crew: %v", err)
	}
	if id == "" {
		t.Fatal("the crew's runtime container must still come up")
	}
	if len(notices) != 1 || notices[0].Kind != NoticeSidecarsUnsupported {
		t.Errorf("notices = %+v, want one %s — a crew silently missing its declared databases is "+
			"the failure this whole package exists to end", notices, NoticeSidecarsUnsupported)
	}
}

func TestStartReportsASidecarFailureAsSuchAndKeepsTheContainerID(t *testing.T) {
	f := &fakeRuntime{servicesErr: errors.New("port already allocated")}
	ctr := &sidecarRuntime{fakeRuntime: f}

	id, err := New(ctr, nil, nil).Start(context.Background(), redisConfig())
	if !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("err = %v, want it to wrap ErrSidecarStart so a caller can tell a sidecar failure "+
			"from a runtime failure", err)
	}
	if id == "" {
		t.Error("the container id must survive a sidecar failure — the runtime IS up, and the " +
			"caller decides whether to proceed without the sidecars")
	}
}

func TestStartFillsWhatTheCallerDidNotResolveAndKeepsWhatItDid(t *testing.T) {
	f := &fakeRuntime{}
	completer := CompleterFunc(func(_ context.Context, cfg provider.CrewConfig) (provider.CrewConfig, error) {
		full := redisConfig()
		full.CachedImage = "crewship-cache:db6c6fcbdb34"
		full.MemoryMB = 8192
		full.TTLHours = 4
		return full, nil
	})

	// What the terminal / container-start route knows: an id, and a caller-set
	// memory limit that must NOT be overwritten by the crew's stored one.
	_, err := New(f, completer, nil).Start(context.Background(), provider.CrewConfig{
		ID:       "crew-1",
		MemoryMB: 2048,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.runtimeCfg.CachedImage != "crewship-cache:db6c6fcbdb34" {
		t.Errorf("CachedImage = %q, want the provisioned tag the caller never looked up (#1717)",
			f.runtimeCfg.CachedImage)
	}
	if len(f.runtimeCfg.Services) != 1 {
		t.Errorf("Services = %+v, want the crew's declared sidecars (#1708)", f.runtimeCfg.Services)
	}
	if f.runtimeCfg.Slug != "data" {
		t.Errorf("Slug = %q, want the crew's slug — the container name derives from it", f.runtimeCfg.Slug)
	}
	if f.runtimeCfg.MemoryMB != 2048 {
		t.Errorf("MemoryMB = %d, want the caller's 2048 — completion fills gaps, it does not "+
			"overrule a caller that decided", f.runtimeCfg.MemoryMB)
	}
	if f.runtimeCfg.TTLHours != 4 {
		t.Errorf("TTLHours = %d, want the crew's 4", f.runtimeCfg.TTLHours)
	}
}

// A completer that cannot answer must not take the crew down with it: that
// would turn one unreachable row into every start path failing at once.
func TestStartSurvivesACompleterFailure(t *testing.T) {
	f := &fakeRuntime{}
	completer := CompleterFunc(func(context.Context, provider.CrewConfig) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, errors.New("database is locked")
	})

	id, err := New(f, completer, nil).Start(context.Background(), provider.CrewConfig{ID: "crew-3", Slug: "s"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id == "" {
		t.Error("the crew must still start from the caller's own config")
	}
}

func TestStartWithoutAContainerProviderIsATypedError(t *testing.T) {
	if _, err := New(nil, nil, nil).Start(context.Background(), provider.CrewConfig{ID: "x"}); !errors.Is(err, ErrNoContainerProvider) {
		t.Errorf("err = %v, want ErrNoContainerProvider", err)
	}
	var nilStarter *Starter
	if _, err := nilStarter.Start(context.Background(), provider.CrewConfig{}); !errors.Is(err, ErrNoContainerProvider) {
		t.Errorf("nil Starter err = %v, want ErrNoContainerProvider", err)
	}
}
