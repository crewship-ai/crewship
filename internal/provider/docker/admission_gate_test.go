package docker

// #1668 — admission control sits at the statements that make a container
// resident, not at the eleven call sites that ask for one.
//
// These tests pin four things the design turns on:
//   - the gate is consulted BEFORE ContainerCreate, not after;
//   - a refusal means no container was created at all;
//   - reusing a container that is ALREADY RUNNING never consults it (that
//     path adds nothing to the host, and queueing it behind a host-memory
//     check would block a chat to a live crew for no reason);
//   - restarting a STOPPED container does consult it (that path does add a
//     container, a netns and its pages back to the host).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// stubGate is a provider.AdmissionGate that records what it was asked and can
// refuse, or report a hold before admitting.
type stubGate struct {
	mu sync.Mutex

	// knobs
	err         error  // non-nil => refuse
	holdReason  string // non-empty => call onHold once before admitting
	holdDetail  string
	holdDelay   time.Duration // waited before onHold fires, so elapsed time is measurable
	beforeAdmit func()        // observed at the moment of the call, for ordering assertions

	// recordings
	crewIDs   []string
	crewSlugs []string
	releases  int
}

func (g *stubGate) Admit(_ context.Context, crewID, crewSlug string, onHold func(reason, detail string)) (func(), error) {
	g.mu.Lock()
	g.crewIDs = append(g.crewIDs, crewID)
	g.crewSlugs = append(g.crewSlugs, crewSlug)
	before := g.beforeAdmit
	reason, detail, delay, err := g.holdReason, g.holdDetail, g.holdDelay, g.err
	g.mu.Unlock()

	if before != nil {
		before()
	}
	if reason != "" && onHold != nil {
		if delay > 0 {
			time.Sleep(delay)
		}
		onHold(reason, detail)
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

func (g *stubGate) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.crewIDs...)
}

func (g *stubGate) releaseCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releases
}

// The structural fix: today runSem is taken inside RunAgent, by which point
// the container exists. The gate must be asked while nothing has been created.
func TestEnsureCrewRuntime_AdmissionPrecedesContainerCreate(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	cfg := covRTConfig(t)
	gate := &stubGate{}

	var createsAtAdmit int
	gate.beforeAdmit = func() {
		f.mu.Lock()
		createsAtAdmit = len(f.creates)
		f.mu.Unlock()
	}
	cfg.Admission = gate

	p := f.provider(t, cfg)
	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	calls := gate.calls()
	if len(calls) != 1 {
		t.Fatalf("admission consulted %d times, want exactly 1: %v", len(calls), calls)
	}
	if calls[0] != "crew1" {
		t.Errorf("admission asked about crew %q, want crew1", calls[0])
	}
	if gate.crewSlugs[0] != "alpha" {
		t.Errorf("admission asked about slug %q, want alpha", gate.crewSlugs[0])
	}
	if createsAtAdmit != 0 {
		t.Fatalf("%d container(s) had already been created when admission was consulted — "+
			"the gate is on the wrong side of the door", createsAtAdmit)
	}
	if gate.releaseCount() != 1 {
		t.Errorf("admission slot released %d times, want 1 — a leaked slot shrinks the bound permanently",
			gate.releaseCount())
	}
}

// A refusal (a cancelled or timed-out hold) must leave the host untouched.
func TestEnsureCrewRuntime_AdmissionRefused_NoContainerIsCreated(t *testing.T) {
	t.Parallel()

	refused := errors.New("held for host capacity: context deadline exceeded")
	f := &covRT{}
	cfg := covRTConfig(t)
	cfg.Admission = &stubGate{err: refused}
	p := f.provider(t, cfg)

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the admission refusal wrapped", err)
	}

	f.mu.Lock()
	n := len(f.creates)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d container(s) created despite a refused admission", n)
	}
}

// The warm path must be free. A crew whose container is already running costs
// the host nothing more; holding that call behind a memory gate would stall a
// chat to a live agent on a busy host, which is the regression this test
// exists to prevent.
func TestEnsureCrewRuntime_ReusingRunningContainer_SkipsAdmission(t *testing.T) {
	t.Parallel()

	f := &covRT{
		listBody:    covExistingList("running"),
		inspectBody: covHealthyInspect(covRuntimeRef),
	}
	cfg := covRTConfig(t)
	gate := &stubGate{}
	cfg.Admission = gate
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Fatalf("reused container id = %q, want old-cid", id)
	}
	if calls := gate.calls(); len(calls) != 0 {
		t.Fatalf("admission consulted %d times for a container that was already running: %v", len(calls), calls)
	}
}

// Restarting a stopped container puts a netns and its pages back on the host,
// so it is admission-controlled exactly like a create.
func TestEnsureCrewRuntime_RestartingStoppedContainer_IsAdmitted(t *testing.T) {
	t.Parallel()

	inspect := map[string]any{}
	_ = json.Unmarshal([]byte(covHealthyInspect(covRuntimeRef)), &inspect)
	inspect["State"] = map[string]any{"Running": false}
	body, _ := json.Marshal(inspect)

	f := &covRT{listBody: covExistingList("exited"), inspectBody: string(body)}
	cfg := covRTConfig(t)
	gate := &stubGate{}
	cfg.Admission = gate
	p := f.provider(t, cfg)

	// The restart path first checks that the host bind dirs still exist;
	// without them it tears the container down and takes the create path
	// instead, which would prove nothing about the restart branch.
	for _, rel := range [][]string{{"workspaces", "crew1"}, {"crew1"}, {"crews", "crew1"}} {
		if err := os.MkdirAll(filepath.Join(append([]string{cfg.OutputBasePath}, rel...)...), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Fatalf("id = %q, want the restarted old-cid (the test fell through to the create path)", id)
	}
	f.mu.Lock()
	started := append([]string(nil), f.starts...)
	f.mu.Unlock()
	if len(started) == 0 {
		t.Fatal("no container was started; the restart branch was not exercised")
	}
	if calls := gate.calls(); len(calls) != 1 {
		t.Fatalf("admission consulted %d times when restarting a stopped container, want 1: %v", len(calls), calls)
	}
	if gate.releaseCount() != 1 {
		t.Errorf("admission slot released %d times on the restart path, want 1", gate.releaseCount())
	}
}

// Visibility: a run held for capacity has to say so on its own provisioning
// stream, which is what the journal and the live WS session read. Silence here
// is what makes a capacity hold indistinguishable from a hang.
func TestEnsureCrewRuntime_CapacityHold_EmitsProvisionEvent(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	cfg := covRTConfig(t)
	cfg.Admission = &stubGate{
		holdReason: "host_memory",
		holdDetail: "host has 900 MiB available, 3072 MiB needed for one more agent container",
		holdDelay:  20 * time.Millisecond,
	}
	p := f.provider(t, cfg)

	var mu sync.Mutex
	var events []devcontainer.ProvisionEvent
	team := covTeam()
	team.ProvisionSink = func(ev devcontainer.ProvisionEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
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
		t.Fatalf("no %q event emitted while the start was held: %+v", devcontainer.ProvStepCapacityHold, events)
	}
	if hold.Phase != devcontainer.ProvisionPhase {
		t.Errorf("hold event phase = %q, want %q", hold.Phase, devcontainer.ProvisionPhase)
	}
	if hold.Detail == "" {
		t.Error("hold event carries no detail; the operator cannot see why")
	}
	// The binding leg travels as a field, not as a prefix glued into Detail:
	// the consumer that turns it into a sentence must not have to parse one
	// back out of prose.
	if hold.Reason != "host_memory" {
		t.Errorf("hold event Reason = %q, want %q", hold.Reason, "host_memory")
	}
	if !strings.Contains(hold.Detail, "900 MiB available") ||
		!strings.Contains(hold.Detail, "3072 MiB needed") {
		t.Errorf("hold event Detail = %q; it does not carry the gate's figures", hold.Detail)
	}
	// Elapsed wait so far. Without it "held for capacity" reads the same at
	// second one and at minute twenty-five, and the caller cannot tell
	// whether waiting longer is worth it.
	if hold.DurationMs < 15 {
		t.Errorf("hold event DurationMs = %d after a 20ms wait; the event does not say how long it has been held",
			hold.DurationMs)
	}
}

// A provider built without a gate — every existing test, and any deployment
// that has not wired one — must behave exactly as it did before.
func TestEnsureCrewRuntime_NoAdmissionConfigured_IsUnchanged(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	cfg := covRTConfig(t)
	if cfg.Admission != nil {
		t.Fatal("covRTConfig must not wire a gate by default")
	}
	p := f.provider(t, cfg)
	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatalf("EnsureCrewRuntime without admission control: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) == 0 {
		t.Fatal("no container created")
	}
}

// The gate is declared on Config so it is fixed before the first ensure can
// run, and so it cannot be lost the way a ContainerProvider decorator would
// lose the nine optional interfaces and the concrete-type assertion in
// internal/server.
var _ provider.AdmissionGate = (*stubGate)(nil)
