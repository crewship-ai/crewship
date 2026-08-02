package apple

// #1668 — the Apple provider is gated at the same two statements as the
// Docker one. It matters more here than it looks: this provider runs on
// macOS, where neither /proc/meminfo nor /proc/pressure/memory exists, so the
// HOST MEMORY leg of admission control is inactive. The concurrency bound and
// the stagger are not — they need no kernel file — and these tests pin that
// the provider still asks, so that when it runs on a host that does publish
// the signal (or when the controller's other legs bite) the answer is honoured.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

type appleStubGate struct {
	mu sync.Mutex

	err         error
	holdReason  string
	beforeAdmit func()

	crewIDs  []string
	releases int
}

func (g *appleStubGate) Admit(_ context.Context, crewID, crewSlug string, onHold func(reason, detail string)) (func(), error) {
	g.mu.Lock()
	g.crewIDs = append(g.crewIDs, crewID+"/"+crewSlug)
	before, reason, err := g.beforeAdmit, g.holdReason, g.err
	g.mu.Unlock()

	if before != nil {
		before()
	}
	if reason != "" && onHold != nil {
		onHold(reason, "detail")
	}
	if err != nil {
		return nil, err
	}
	return func() {
		g.mu.Lock()
		g.releases++
		g.mu.Unlock()
	}, nil
}

func (g *appleStubGate) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.crewIDs...)
}

func (g *appleStubGate) releaseCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releases
}

var _ provider.AdmissionGate = (*appleStubGate)(nil)

func TestAppleEnsureCrewRuntime_AdmissionPrecedesCreate(t *testing.T) {
	fake := installFakeContainer(t, crewBody)
	gate := &appleStubGate{}

	var createdAtAdmit bool
	gate.beforeAdmit = func() {
		createdAtAdmit = fake.hasCall(t, "create")
	}

	p := newTestProvider(Config{OutputBasePath: t.TempDir(), Admission: gate})
	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"}); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	if calls := gate.calls(); len(calls) != 1 || calls[0] != "crew1/eng" {
		t.Fatalf("admission calls = %v, want exactly [crew1/eng]", calls)
	}
	if createdAtAdmit {
		t.Fatal("the container had already been created when admission was consulted")
	}
	if !fake.hasCall(t, "create") {
		t.Fatal("no create happened; the test proved nothing")
	}
	if gate.releaseCount() != 1 {
		t.Errorf("released %d times, want 1", gate.releaseCount())
	}
}

func TestAppleEnsureCrewRuntime_AdmissionRefused_NoCreate(t *testing.T) {
	fake := installFakeContainer(t, crewBody)
	refused := errors.New("held for host capacity")
	p := newTestProvider(Config{OutputBasePath: t.TempDir(), Admission: &appleStubGate{err: refused}})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the refusal wrapped", err)
	}
	if fake.hasCall(t, "create") {
		t.Fatal("a container was created despite a refused admission")
	}
}

// Already running: free, same as Docker.
func TestAppleEnsureCrewRuntime_ReusingRunningContainer_SkipsAdmission(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  network) if [ "$2" = "list" ]; then echo '[{"name":"mynet"}]'; fi; exit 0;;
  list) echo '[{"status":"running","configuration":{"id":"crewship-team-eng-crew1"}}]'; exit 0;;
esac
exit 0`)
	gate := &appleStubGate{}
	p := newTestProvider(Config{OutputBasePath: t.TempDir(), Admission: gate})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "crewship-team-eng-crew1" {
		t.Fatalf("id = %q, want the reused running container", id)
	}
	if calls := gate.calls(); len(calls) != 0 {
		t.Fatalf("admission consulted %v for an already-running container", calls)
	}
}

// Restarting a stopped container is gated, because it puts the container back
// on the host.
func TestAppleEnsureCrewRuntime_RestartingStoppedContainer_IsAdmitted(t *testing.T) {
	base := t.TempDir()
	installFakeContainer(t, `
case "$1" in
  network) if [ "$2" = "list" ]; then echo '[{"name":"mynet"}]'; fi; exit 0;;
  list) echo '[{"status":"stopped","configuration":{"id":"crewship-team-eng-crew1"}}]'; exit 0;;
  start) exit 0;;
esac
exit 0`)
	gate := &appleStubGate{}
	p := newTestProvider(Config{OutputBasePath: base, Admission: gate})

	// The restart branch is only reached when the host bind dirs survive.
	for _, d := range []string{"workspaces/crew1", "crew1", "crews/crew1"} {
		if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "crewship-team-eng-crew1" {
		t.Fatalf("id = %q, want the restarted container (fell through to create)", id)
	}
	if calls := gate.calls(); len(calls) != 1 {
		t.Fatalf("admission consulted %v when restarting a stopped container, want exactly one call", calls)
	}
}

// Parity with the Docker provider on the one event that matters here (#1675).
// This provider deliberately emits no other container-preparation event, so
// it would be easy to read the hold's absence as consistent — it is not. The
// host-memory leg is inactive on macOS, but the concurrency bound and the
// stagger bind here, so a start CAN be held, and a hold nobody can see is
// indistinguishable from a hang on any platform.
func TestAppleEnsureCrewRuntime_CapacityHold_ReachesTheProvisionSink(t *testing.T) {
	installFakeContainer(t, crewBody)
	gate := &appleStubGate{holdReason: "concurrency"}

	var mu sync.Mutex
	var events []devcontainer.ProvisionEvent
	cfg := provider.CrewConfig{ID: "crew1", Slug: "eng", ProvisionSink: func(ev devcontainer.ProvisionEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}}

	p := newTestProvider(Config{OutputBasePath: t.TempDir(), Admission: gate})
	if _, err := p.EnsureCrewRuntime(context.Background(), cfg); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var hold *devcontainer.ProvisionEvent
	for i := range events {
		if events[i].Step == devcontainer.ProvStepCapacityHold {
			hold = &events[i]
			break
		}
	}
	if hold == nil {
		t.Fatalf("the apple provider held a start and said nothing on the run's own stream: %+v", events)
	}
	if hold.Phase != devcontainer.ProvisionPhase {
		t.Errorf("hold event phase = %q, want %q", hold.Phase, devcontainer.ProvisionPhase)
	}
	if hold.Reason != "concurrency" {
		t.Errorf("hold event Reason = %q, want %q", hold.Reason, "concurrency")
	}
	if hold.Detail == "" {
		t.Error("hold event carries no detail; the caller cannot see why")
	}
	// And nothing else: this provider's asymmetry is deliberate, and a test
	// that only checked "a hold arrived" would not notice it quietly growing
	// a half-populated audit trail.
	if len(events) != 1 {
		t.Errorf("the apple provider emitted %d provisioning events, want only the capacity hold: %+v",
			len(events), events)
	}
}

func TestAppleEnsureCrewRuntime_NoAdmissionConfigured_IsUnchanged(t *testing.T) {
	fake := installFakeContainer(t, crewBody)
	p := newTestProvider(Config{OutputBasePath: t.TempDir()})
	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"}); err != nil {
		t.Fatalf("EnsureCrewRuntime without admission control: %v", err)
	}
	if !fake.hasCall(t, "create") {
		t.Fatalf("no create with a nil gate; calls = %v", strings.Join(fake.calls(t), " | "))
	}
}
