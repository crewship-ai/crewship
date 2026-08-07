package orchestrator

// Coverage for the idle-crew reaper (#1662).
//
// Before this, `checkTTLs` had exactly one input — an in-process map that a
// `crewshipd` restart emptied — and exactly one rule: stop anything whose
// last recorded activity is older than a TTL that any run carrying 0 could
// silently erase. These tests pin the four things that replaced it:
//
//   1. the TTL is resolved from the crews table on every sweep, so no run can
//      clobber it and no config change has to wait for a restart;
//   2. a hold taken by any of the four in-container occupants blocks the stop;
//   3. a detached tmux session is confirmed by a single exec probe, and only
//      for crews the sweep has already decided to stop;
//   4. the stop names the crew container and nothing else — service sidecars
//      are separate containers and Crewship does not manage them.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// A container provider that can answer the tmux probe and that also implements
// SidecarProvider, so a test can prove the reaper never reaches for it.
// ---------------------------------------------------------------------------

type reapFakeContainer struct {
	mu sync.Mutex

	execCalls   int
	execCmds    [][]string
	execTargets []string
	execErr     error

	inspectRunning  bool
	inspectExitCode int
	inspectErr      error

	stopCalls  int
	stoppedIDs []string

	// Sidecar surface. Any non-zero count here is a regression: the reaper
	// must never touch a crew's service containers.
	ensureServicesCalls int
	stopServicesCalls   int
	removeServicesCalls int
}

func (f *reapFakeContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (f *reapFakeContainer) StopCrewRuntime(_ context.Context, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	f.stoppedIDs = append(f.stoppedIDs, containerID)
	return nil
}
func (f *reapFakeContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *reapFakeContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (f *reapFakeContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *reapFakeContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls++
	f.execCmds = append(f.execCmds, cfg.Cmd)
	f.execTargets = append(f.execTargets, cfg.ContainerID)
	if f.execErr != nil {
		return nil, f.execErr
	}
	return &provider.ExecResult{ExecID: "exec-1", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (f *reapFakeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inspectRunning, f.inspectExitCode, f.inspectErr
}
func (f *reapFakeContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (f *reapFakeContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

func (f *reapFakeContainer) EnsureCrewServices(_ context.Context, _ provider.CrewConfig) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureServicesCalls++
	return nil, nil
}
func (f *reapFakeContainer) StopCrewServices(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopServicesCalls++
	return nil
}
func (f *reapFakeContainer) RemoveCrewServices(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeServicesCalls++
	return nil
}

var (
	_ provider.ContainerProvider = (*reapFakeContainer)(nil)
	_ provider.SidecarProvider   = (*reapFakeContainer)(nil)
)

// noTmuxFake is the common case: `tmux ls` runs and exits 1 ("no server
// running"), so nothing holds the container open.
func noTmuxFake() *reapFakeContainer {
	return &reapFakeContainer{inspectRunning: false, inspectExitCode: 1}
}

func (f *reapFakeContainer) snapshotStops() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalls, append([]string(nil), f.stoppedIDs...)
}

func (f *reapFakeContainer) snapshotExecs() (int, [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execCalls, append([][]string(nil), f.execCmds...)
}

// staleCrew registers a crew whose recorded activity is well past any TTL a
// test sets, so the sweep always reaches the stop decision.
func staleCrew(o *Orchestrator, crewID, containerID string, ttl time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.crews[crewID] = &crewState{
		lastActivity: time.Now().Add(-48 * time.Hour),
		ttl:          ttl,
		containerID:  containerID,
	}
}

// ---------------------------------------------------------------------------
// Defect 2 — last-writer-wins: a run carrying TTL 0 erased a real TTL.
// ---------------------------------------------------------------------------

func TestRefreshActivity_UnknownTTLDoesNotClobberRegisteredTTL(t *testing.T) {
	// routes_agent.go reads ttl_hours straight off the HTTP body and
	// defaults it to 0, so "the caller said nothing" and "the operator
	// asked for no TTL" arrived at refreshActivity as the same value —
	// and the second one won. A non-positive ttlHours now carries no
	// information and must leave a registered TTL standing.
	o := New(noTmuxFake(), nil, quietLifecycleLogger())

	o.refreshActivity("crew-a", "ct-a", 6)
	o.refreshActivity("crew-a", "ct-a", 0)

	o.mu.RLock()
	got := o.crews["crew-a"].ttl
	o.mu.RUnlock()
	if got != 6*time.Hour {
		t.Errorf("ttl after a TTL-0 refresh = %v, want 6h (unchanged)", got)
	}
}

func TestRefreshActivity_PositiveTTLReplacesRegisteredTTL(t *testing.T) {
	// The non-clobber rule must not freeze the TTL: a later run that DOES
	// carry a value still wins.
	o := New(noTmuxFake(), nil, quietLifecycleLogger())
	o.refreshActivity("crew-a", "ct-a", 6)
	o.refreshActivity("crew-a", "ct-a", 2)

	o.mu.RLock()
	got := o.crews["crew-a"].ttl
	o.mu.RUnlock()
	if got != 2*time.Hour {
		t.Errorf("ttl = %v, want 2h", got)
	}
}

func TestRefreshActivity_EmptyContainerIDDoesNotBlankTheRecordedContainer(t *testing.T) {
	// A clock bump that doesn't know the container id (a hold release)
	// must not erase the id the reaper needs to issue the stop — an empty
	// id makes the crew unreapable forever.
	o := New(noTmuxFake(), nil, quietLifecycleLogger())
	o.refreshActivity("crew-a", "ct-a", 6)
	o.refreshActivity("crew-a", "", 6)

	o.mu.RLock()
	got := o.crews["crew-a"].containerID
	o.mu.RUnlock()
	if got != "ct-a" {
		t.Errorf("containerID = %q, want ct-a", got)
	}
}

// ---------------------------------------------------------------------------
// The TTL resolver — the crews table is the authority, read on every sweep.
// ---------------------------------------------------------------------------

func TestCheckTTLs_ResolverIsAuthoritativeOverTheRegisteredTTL(t *testing.T) {
	// The in-process value said 100h (nowhere near expiry); the crews table
	// says 1h. The row wins, so the crew is reaped in the same sweep the
	// operator's edit lands — no daemon restart required.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 100*time.Hour)
	o.SetCrewTTLResolver(func(context.Context) map[string]int {
		return map[string]int{"crew-a": 1}
	})

	o.checkTTLs(context.Background())

	stops, ids := fake.snapshotStops()
	if stops != 1 || len(ids) != 1 || ids[0] != "ct-a" {
		t.Errorf("stops = %d %v, want 1 [ct-a] — the resolver's 1h must beat the registered 100h", stops, ids)
	}
}

func TestCheckTTLs_ResolverSaysNeverStop_ContainerKeptRunning(t *testing.T) {
	// An explicit container_ttl_hours = 0 is "never stop". The registered
	// in-process TTL must not be able to override the row in that
	// direction either.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)
	o.SetCrewTTLResolver(func(context.Context) map[string]int {
		return map[string]int{"crew-a": 0}
	})

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("StopCrewRuntime called %d times for a never-stop crew; want 0", stops)
	}
	o.mu.RLock()
	_, kept := o.crews["crew-a"]
	o.mu.RUnlock()
	if !kept {
		t.Error("never-stop crew evicted from the tracking map")
	}
}

func TestCheckTTLs_ResolverOmitsCrew_ContainerKeptRunning(t *testing.T) {
	// A crew the resolver cannot see (row deleted, DB unreachable and the
	// resolver returned a partial map) must not be reaped on the strength
	// of a stale in-process TTL. Unknown fails safe.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)
	o.SetCrewTTLResolver(func(context.Context) map[string]int {
		return map[string]int{"crew-other": 1}
	})

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("StopCrewRuntime called %d times for a crew the resolver never mentioned; want 0", stops)
	}
}

// ---------------------------------------------------------------------------
// Holds — the four in-container occupants.
// ---------------------------------------------------------------------------

func TestCheckTTLs_HeldContainer_NotStopped(t *testing.T) {
	// A running script step / terminal attach / agent run holds the
	// container. The idle clock is long past the TTL; the hold still wins.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	release := o.RetainCrewContainer("crew-a", "ct-a")
	o.mu.Lock()
	o.crews["crew-a"].lastActivity = time.Now().Add(-48 * time.Hour)
	o.mu.Unlock()

	o.checkTTLs(context.Background())
	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Fatalf("held container stopped %d times; want 0", stops)
	}

	release()

	// After the last occupant leaves, the idle clock restarts — the crew
	// is not instantly reapable on the next tick.
	o.checkTTLs(context.Background())
	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("container stopped %d times immediately after the hold was released; want 0 (clock restarts)", stops)
	}
}

func TestRetainCrewContainer_NestedHolds_OnlyTheLastReleaseFrees(t *testing.T) {
	// Two overlapping occupants (a chat run and a script step) in one
	// container. The first release must not unpin it.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())

	releaseA := o.RetainCrewContainer("crew-a", "ct-a")
	releaseB := o.RetainCrewContainer("crew-a", "ct-a")

	releaseA()
	o.mu.RLock()
	after1 := o.crews["crew-a"].holds
	o.mu.RUnlock()
	if after1 != 1 {
		t.Errorf("holds after first release = %d, want 1", after1)
	}

	releaseB()
	o.mu.RLock()
	after2 := o.crews["crew-a"].holds
	o.mu.RUnlock()
	if after2 != 0 {
		t.Errorf("holds after second release = %d, want 0", after2)
	}
}

func TestRetainCrewContainer_ReleaseIsIdempotent(t *testing.T) {
	// A deferred release that also runs on an error path must not drive
	// the count negative and unpin a sibling occupant's hold.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())

	release := o.RetainCrewContainer("crew-a", "ct-a")
	other := o.RetainCrewContainer("crew-a", "ct-a")
	release()
	release()
	release()

	o.mu.RLock()
	holds := o.crews["crew-a"].holds
	o.mu.RUnlock()
	if holds != 1 {
		t.Errorf("holds after three calls to one release func = %d, want 1 (the sibling hold survives)", holds)
	}
	other()
}

// ---------------------------------------------------------------------------
// The tmux probe — observed, not held, because a detached session outlives
// both the run that started it and the daemon that started the run.
// ---------------------------------------------------------------------------

func TestCheckTTLs_LiveTmuxSession_KeepsContainerRunning(t *testing.T) {
	// `tmux ls` exit 0 = at least one session. secrets_cleanup.go already
	// documents that such a run "keeps its hold forever"; stopping the
	// container would kill it.
	fake := &reapFakeContainer{inspectRunning: false, inspectExitCode: 0}
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("container with a live tmux session stopped %d times; want 0", stops)
	}
	_, cmds := fake.snapshotExecs()
	if len(cmds) != 1 || len(cmds[0]) < 2 || cmds[0][0] != "tmux" {
		t.Errorf("probe cmd = %v, want a single tmux invocation", cmds)
	}
}

func TestCheckTTLs_NoTmuxSession_Stops(t *testing.T) {
	fake := noTmuxFake() // exit 1 — "no server running"
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	o.checkTTLs(context.Background())

	stops, ids := fake.snapshotStops()
	if stops != 1 || len(ids) != 1 || ids[0] != "ct-a" {
		t.Errorf("stops = %d %v, want 1 [ct-a]", stops, ids)
	}
}

func TestCheckTTLs_TmuxNotInstalled_Stops(t *testing.T) {
	// Exit 127 is "tmux: not found" on a BYOI image — there cannot be a
	// session, so the container is genuinely idle.
	fake := &reapFakeContainer{inspectRunning: false, inspectExitCode: 127}
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 1 {
		t.Errorf("stops = %d, want 1 — exit 127 means no tmux, not a busy container", stops)
	}
}

func TestCheckTTLs_TmuxProbeExecFails_KeepsContainerRunning(t *testing.T) {
	// We could not ask, so we do not stop. Failing safe here costs an idle
	// container until the next tick; failing open costs a killed agent.
	fake := noTmuxFake()
	fake.execErr = errors.New("daemon unreachable")
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("container stopped %d times after an unanswerable probe; want 0", stops)
	}
}

func TestCheckTTLs_TmuxProbeInspectFails_KeepsContainerRunning(t *testing.T) {
	fake := noTmuxFake()
	fake.inspectErr = errors.New("exec inspect failed")
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("container stopped %d times after an unreadable probe result; want 0", stops)
	}
}

func TestCheckTTLs_ProbesOnlyCrewsItHasAlreadyDecidedToStop(t *testing.T) {
	// The sweep runs on a timer against every tracked crew. Probing each
	// one costs a docker exec, and the PRD measures exec polling at ~42%
	// of dockerd's serialized exec capacity at 50 crews. The probe is a
	// confirmation at the moment of stopping, not a poll: a crew inside
	// its TTL must cost zero execs.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-expired", "ct-expired", 1*time.Hour)
	o.mu.Lock()
	o.crews["crew-fresh"] = &crewState{
		lastActivity: time.Now(),
		ttl:          1 * time.Hour,
		containerID:  "ct-fresh",
	}
	o.crews["crew-held"] = &crewState{
		lastActivity: time.Now().Add(-48 * time.Hour),
		ttl:          1 * time.Hour,
		containerID:  "ct-held",
		holds:        1,
	}
	o.mu.Unlock()

	o.checkTTLs(context.Background())

	execs, _ := fake.snapshotExecs()
	if execs != 1 {
		t.Errorf("exec probes = %d across 3 tracked crews, want 1 (only the expired, unheld one)", execs)
	}
	if fake.execTargets[0] != "ct-expired" {
		t.Errorf("probed %q, want ct-expired", fake.execTargets[0])
	}
}

// ---------------------------------------------------------------------------
// The busy probe — a live exposed port. Observed rather than held because the
// port-exposure registry rehydrates from the DB and therefore outlives the
// process that took any hold.
// ---------------------------------------------------------------------------

func TestCheckTTLs_BusyProbeVeto_KeepsContainerRunning(t *testing.T) {
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	var probedCrew, probedContainer string
	o.SetContainerBusyProbe(func(_ context.Context, crewID, containerID string) bool {
		probedCrew, probedContainer = crewID, containerID
		return true
	})

	o.checkTTLs(context.Background())

	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("container with a live exposed port stopped %d times; want 0", stops)
	}
	if probedCrew != "crew-a" || probedContainer != "ct-a" {
		t.Errorf("busy probe args = (%q, %q), want (crew-a, ct-a)", probedCrew, probedContainer)
	}
}

func TestCheckTTLs_BusyProbeVeto_SkipsTheTmuxExecEntirely(t *testing.T) {
	// The injected probe is a map lookup; the tmux probe is a docker exec.
	// Cheapest veto first.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)
	o.SetContainerBusyProbe(func(context.Context, string, string) bool { return true })

	o.checkTTLs(context.Background())

	if execs, _ := fake.snapshotExecs(); execs != 0 {
		t.Errorf("exec probes = %d after an in-memory veto; want 0", execs)
	}
}

// ---------------------------------------------------------------------------
// Scope — the crew container, and nothing else.
// ---------------------------------------------------------------------------

func TestCheckTTLs_StopsTheCrewContainerOnly_NeverServiceSidecars(t *testing.T) {
	// A crew's redis/postgres are separate containers with their own names,
	// restart policies and labels, joined to the crew's bridge network
	// (provider/docker/sidecar.go ensureSidecar). They are neighbours of
	// the crew container, not residents of it. Reaping an idle agent
	// runtime must not reach for them — Crewship stops the runtime it owns
	// and does not manage anyone else's infrastructure.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	staleCrew(o, "crew-a", "ct-a", 1*time.Hour)

	o.checkTTLs(context.Background())

	stops, ids := fake.snapshotStops()
	if stops != 1 || len(ids) != 1 || ids[0] != "ct-a" {
		t.Fatalf("stopped %v, want exactly [ct-a]", ids)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.stopServicesCalls != 0 || fake.removeServicesCalls != 0 || fake.ensureServicesCalls != 0 {
		t.Errorf("sidecar surface touched: ensure=%d stop=%d remove=%d; want 0/0/0",
			fake.ensureServicesCalls, fake.stopServicesCalls, fake.removeServicesCalls)
	}
}

// ---------------------------------------------------------------------------
// Defect 1 — the clock was born in process and a restart reset it.
// ---------------------------------------------------------------------------

func TestSeedCrewActivity_ClockStartsAtContainerStartNotNow(t *testing.T) {
	// A container that survived a crewshipd restart was registered with the
	// stats collector only, so the reaper never saw it — dev1 had one that
	// had been running five days with zero agent runs. Boot now seeds the
	// clock from the container's own StartedAt, which Docker keeps and no
	// daemon restart can reset. Seeding it with `now` instead would hand
	// every restart a fresh TTL window, and on a host that redeploys more
	// often than the TTL nothing would ever be reaped.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())

	started := time.Now().Add(-5 * 24 * time.Hour)
	o.SeedCrewActivity("crew-a", "ct-a", 4, started)

	o.mu.RLock()
	got := o.crews["crew-a"].lastActivity
	o.mu.RUnlock()
	if got.Sub(started).Abs() > time.Second {
		t.Fatalf("seeded lastActivity = %v, want the container start time %v", got, started)
	}

	o.checkTTLs(context.Background())
	if stops, _ := fake.snapshotStops(); stops != 1 {
		t.Errorf("5-day-old rehydrated container stopped %d times, want 1", stops)
	}
}

func TestSeedCrewActivity_DoesNotOverwriteALiveInProcessClock(t *testing.T) {
	// Seeding is a boot-time floor, not a reset: if this process already
	// knows about the crew (it was woken between rehydration passes), the
	// live clock is strictly better information than StartedAt.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())

	o.refreshActivity("crew-a", "ct-a", 4)
	o.SeedCrewActivity("crew-a", "ct-a", 4, time.Now().Add(-5*24*time.Hour))

	o.checkTTLs(context.Background())
	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("a just-active crew was stopped %d times by a boot seed; want 0", stops)
	}
}

func TestSeedCrewActivity_ZeroTTLIsRecordedAsNeverStop(t *testing.T) {
	// Boot is the one path that reads the row directly, so it is the one
	// path allowed to record an explicit "never stop" — unlike a run's
	// ttl_hours, a 0 here is a value, not a missing field.
	fake := noTmuxFake()
	o := New(fake, nil, quietLifecycleLogger())
	o.SeedCrewActivity("crew-a", "ct-a", 0, time.Now().Add(-5*24*time.Hour))

	o.checkTTLs(context.Background())
	if stops, _ := fake.snapshotStops(); stops != 0 {
		t.Errorf("never-stop crew stopped %d times after a boot seed; want 0", stops)
	}
}
