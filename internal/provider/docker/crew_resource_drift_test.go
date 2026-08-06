package docker

// Per-crew resource drift: the crew's memory / CPU limit moved after its
// container was created (#1681).
//
// #1642's contract digest deliberately cannot see this — it is computed over a
// CANONICAL crew so that it tracks the BUILD, not the crew, and a per-crew
// number moving must not move it. This is the other half: the operator edits
// `crewship crew update <slug> --memory-mb 8192`, the row changes, `crew get`
// reports the new figure, and the running container keeps the old cgroup limit
// with nothing anywhere saying so.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covResourceInspect is a healthy, current-contract container carrying
// explicit cgroup limits — the thing a per-crew comparison has to read.
// running distinguishes the two halves of the response asymmetry.
func covResourceInspect(running bool, memoryMB int, cpus float64) string {
	b, _ := json.Marshal(map[string]any{
		"Id":    "old-cid",
		"State": map[string]any{"Running": running},
		"Config": map[string]any{
			"Image":  covRuntimeRef,
			"Labels": map[string]string{crewRuntimeContractLabel: covContractDigest()},
		},
		"Mounts": []map[string]any{
			{"Destination": "/crew"},
			{"Destination": "/home/agent"},
			{"Destination": "/opt/crew-tools"},
		},
		"HostConfig": map[string]any{
			"Tmpfs":    map[string]string{"/secrets": secretsTmpfsSpec},
			"Memory":   int64(memoryMB) * 1024 * 1024,
			"NanoCPUs": int64(cpus * 1e9),
		},
	})
	return string(b)
}

// covSizedTeam is covTeam with the resource limits an operator configured —
// what the resolver passes when it read them off the crews row.
func covSizedTeam(memoryMB int, cpus float64) provider.CrewConfig {
	t := covTeam()
	t.MemoryMB = memoryMB
	t.CPUs = cpus
	return t
}

// A STOPPED container whose cgroup limits no longer match the crew's
// configuration is replaced rather than started.
//
// This is the moment the recreate is free — nothing is executing inside it and
// the caller is already paying for a start — and it is the only moment a new
// memory limit can take effect at all, because Memory and NanoCPUs are applied
// at ContainerCreate. Without this, `crew update --memory-mb` is a row change
// that never reaches the runtime: the crew idles, stops, wakes, and comes back
// with exactly the limit it had before.
func TestEnsureCrewRuntime_StoppedContainerWithStaleMemoryLimitIsRecreated(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateExited)),
		inspectBody: covResourceInspect(false, 4096, 2),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covSizedTeam(8192, 2))
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id == "old-cid" {
		t.Fatal("the stopped container was started as-is; its cgroup memory limit is still the old 4096 MiB, " +
			"and a limit can only change at ContainerCreate — so the crew would never pick the new one up")
	}

	// realCreate takes f.mu itself, so it runs before the lock below.
	if got := f.realCreate(t).HostConfig.Resources.Memory; got != 8192*1024*1024 {
		t.Errorf("replacement Memory = %d bytes, want %d (the configured 8192 MiB)", got, 8192*1024*1024)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	removed := false
	for _, d := range f.deletes {
		if d == "old-cid" {
			removed = true
		}
	}
	if !removed {
		t.Errorf("stale-sized container was not removed, deletes = %v", f.deletes)
	}
	if len(f.creates) == 0 {
		t.Error("no replacement container was created")
	}
}

// The CPU half of the same rule. Separate test rather than a table row because
// the two limits arrive through different fields and a check that reads only
// Memory passes the memory case and silently ignores this one.
func TestEnsureCrewRuntime_StoppedContainerWithStaleCPULimitIsRecreated(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateExited)),
		inspectBody: covResourceInspect(false, 4096, 2),
	}
	p := f.provider(t, cfg)

	if _, err := p.EnsureCrewRuntime(context.Background(), covSizedTeam(4096, 4)); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	f.mu.Lock()
	created := len(f.creates)
	f.mu.Unlock()
	if created == 0 {
		t.Fatal("a stopped container limited to 2 CPUs was reused for a crew configured with 4")
	}
	if got := f.realCreate(t).HostConfig.Resources.NanoCPUs; got != int64(4*1e9) {
		t.Errorf("replacement NanoCPUs = %d, want %d", got, int64(4*1e9))
	}
}

// A RUNNING container is left alone, exactly as #1642 leaves one carrying an
// older contract.
//
// This is the deliberate half. Tearing down a live crew container SIGKILLs
// whatever is executing in it, and a resize is typically made ahead of time
// rather than urgently — unlike a network-policy change, where the old policy
// is a live security exposure and the stop is worth the killed run. The gap is
// reported instead, on `crewship crew container-status`, and closes on the
// next recreate.
func TestEnsureCrewRuntime_RunningContainerWithStaleLimitsIsNotKilled(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateRunning)),
		inspectBody: covResourceInspect(true, 4096, 2),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covSizedTeam(8192, 4))
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid — a running crew must keep serving a resize it cannot apply yet", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deletes) != 0 {
		t.Errorf("a RUNNING crew container was torn down over a memory-limit edit (deletes = %v); "+
			"every agent executing in it dies with exit 137", f.deletes)
	}
	if len(f.creates) != 0 {
		t.Errorf("a replacement was created while the old container was still running (%d creates)", len(f.creates))
	}
}

// The negative half: limits that already agree must not provoke a rebuild, or
// every idle-TTL wake recreates every crew container forever.
func TestEnsureCrewRuntime_StoppedContainerWithMatchingLimitsIsStarted(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateExited)),
		inspectBody: covResourceInspect(false, 8192, 2),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covSizedTeam(8192, 2))
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid — a container already carrying the configured limits is started, not rebuilt", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 0 {
		t.Errorf("container matching the configured limits was rebuilt anyway (%d creates)", len(f.creates))
	}
}

// The trap this comparison has to survive, and the reason it is gated on the
// caller having stated a size at all.
//
// Not every caller carries the crew's configuration: the assignment path's
// bare-config callers pass ID + Slug and nothing else, and EnsureCrewRuntime
// substitutes its 8192 MiB / 2 CPU floor for them AFTER the reconcile. A
// comparison that ran on those defaults would read a 4096 MiB crew as drifted
// and rebuild it — the same shape of bug the image-drift path had to grow
// callerSpecifiedImage for, where a bare-config caller clobbered a lead's run
// mid-flight.
func TestEnsureCrewRuntime_BareConfigCallerDoesNotProvokeAResourceRebuild(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateExited)),
		inspectBody: covResourceInspect(false, 4096, 2),
	}
	p := f.provider(t, cfg)

	// covTeam carries no MemoryMB/CPUs at all — the bare-config caller.
	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 0 || len(f.deletes) != 0 {
		t.Errorf("a caller that stated no size rebuilt the crew container against the provider default "+
			"(%d creates, deletes = %v); it has no idea what the crew is configured for", len(f.creates), f.deletes)
	}
}

// The other half of the same rule, from the container's side: a container that
// declares NO limit is saying nothing, not saying zero.
//
// Comparing a stated 8192 MiB against an absent limit and calling it drift
// would rebuild such a container on every single wake — and a provider or an
// API version whose inspect does not report HostConfig limits would put every
// crew on the host into that loop. "No opinion is not drift" is the same rule
// the contract digest follows when it cannot compute itself, and it costs
// nothing real: a container created by this build always carries both limits,
// so one that carries neither predates them — which is exactly what the
// contract check already rebuilds a stopped container for.
func TestEnsureCrewRuntime_ContainerWithNoDeclaredLimitsIsNotDrift(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody: covExistingList(string(container.StateExited)),
		// Current contract, every required mount, and no Memory / NanoCPUs.
		inspectBody: covLabelledInspect(covRuntimeRef, false, map[string]string{
			crewRuntimeContractLabel: covContractDigest(),
		}),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covSizedTeam(8192, 2))
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 0 || len(f.deletes) != 0 {
		t.Errorf("a container that reported no limits at all was treated as drifted "+
			"(%d creates, deletes = %v); on a provider that never reports them, that is every crew, "+
			"on every wake, forever", len(f.creates), f.deletes)
	}
}

// The report. `crewship crew container-status` is where an operator finds out
// what the RUNNING container actually has, so the numbers have to reach
// provider.ContainerStatus — read off the inspect, never recomputed from a
// spec. An observation cannot disagree with the builder; a second
// reconstruction can, which is the failure mode #1642 refused to introduce
// into this surface.
func TestContainerStatus_ReportsTheContainersOwnLimits(t *testing.T) {
	t.Parallel()

	f := &covRT{inspectBody: covResourceInspect(true, 4096, 2)}
	p := f.provider(t, covRTConfig(t))

	st, err := p.ContainerStatus(context.Background(), "old-cid")
	if err != nil {
		t.Fatalf("ContainerStatus: %v", err)
	}
	if st.MemoryMB != 4096 {
		t.Errorf("MemoryMB = %d, want 4096 — read from the container's own HostConfig", st.MemoryMB)
	}
	if st.CPUs != 2 {
		t.Errorf("CPUs = %v, want 2", st.CPUs)
	}
}

// A container created without explicit limits reports nothing rather than
// zero: "unlimited" and "we could not tell" must not both render as 0 next to
// a configured 8192, which would invent a drift report out of an inspect that
// simply had nothing to say.
func TestContainerStatus_OmitsLimitsTheContainerDoesNotCarry(t *testing.T) {
	t.Parallel()

	f := &covRT{inspectBody: covLabelledInspect(covRuntimeRef, true, map[string]string{"crewship.kind": "crew"})}
	p := f.provider(t, covRTConfig(t))

	st, err := p.ContainerStatus(context.Background(), "old-cid")
	if err != nil {
		t.Fatalf("ContainerStatus: %v", err)
	}
	if st.MemoryMB != 0 || st.CPUs != 0 {
		t.Errorf("MemoryMB/CPUs = %d/%v, want 0/0 for a container carrying no limits", st.MemoryMB, st.CPUs)
	}
}
