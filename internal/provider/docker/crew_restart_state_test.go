package docker

// #1636 defect 2: `restarting` became a reachable state for crew containers
// when they gained a RestartPolicy, and reconcileExistingContainer could not
// see it.
//
// The old code tested `c.State == "running"` and, for anything else, called
// ContainerStart. For a container in restart backoff the daemon answers
// 304 Not Modified (State.Running is true throughout backoff), which the moby
// Go client reports as a nil error — so EnsureCrewRuntime returned
// (id, true, nil), logged "restarted stopped container", and setWarm()'d it.
// Every agent exec for the whole warm-TTL window then failed with "container
// is restarting" while the provider reported the crew ready.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// covCrewBindDirs creates the host bind dirs reconcileExistingContainer stats
// before it will reuse a container, so a test that gets past the mount checks
// reaches the start/reuse decision rather than the missing-binds recreate.
func covCrewBindDirs(t *testing.T, cfg Config) {
	t.Helper()
	for _, d := range []string{
		filepath.Join(cfg.OutputBasePath, "workspaces", "crew1"),
		filepath.Join(cfg.OutputBasePath, "crew1"),
		filepath.Join(cfg.OutputBasePath, "crews", "crew1"),
		filepath.Join(cfg.OutputBasePath, "secrets", "crew1"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// covRestartingInspect is covHealthyInspect for a container the daemon is
// restarting: every mount is intact (so nothing else triggers a recreate) and
// State reports Running AND Restarting, which is what moby reports during
// restart backoff and is exactly why a bare Running check is not enough.
func covRestartingInspect(image string) string {
	b, _ := json.Marshal(map[string]any{
		"Id":     "old-cid",
		"State":  map[string]any{"Running": true, "Restarting": true},
		"Config": map[string]any{"Image": image},
		"Mounts": []map[string]any{
			{"Destination": "/crew"},
			{"Destination": "/home/agent"},
			{"Destination": "/opt/crew-tools"},
		},
		"HostConfig": map[string]any{
			"Tmpfs": map[string]string{"/secrets": secretsTmpfsSpec},
		},
	})
	return string(b)
}

// A container in restart backoff must be torn down and recreated, never
// "started" and reported ready. The `startFn` here is the teeth: it fails any
// start of old-cid, so if the reconcile path ever goes back to starting a
// restarting container the test fails loudly rather than by assertion order.
func TestEnsureCrewRuntime_RestartingContainerIsRecreated(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateRestarting)),
		inspectBody: covRestartingInspect(covRuntimeRef),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id == "old-cid" {
		t.Fatalf("id = old-cid: a container in restart backoff was handed back as ready; every exec against it fails with \"container is restarting\"")
	}
	if id != "cov-cid-0123456789ab" {
		t.Errorf("id = %q, want the freshly created container", id)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) == 0 {
		t.Error("no container was created; the restarting container must be replaced, not reused")
	}
	found := false
	for _, d := range f.deletes {
		if d == "old-cid" {
			found = true
		}
	}
	if !found {
		t.Errorf("restarting container was not removed, deletes = %v", f.deletes)
	}
	for _, s := range f.starts {
		if s == "old-cid" {
			t.Errorf("ContainerStart was issued against the restarting container (starts = %v); the daemon answers 304 and the nil error reads as success", f.starts)
		}
	}
}

// The inspect is the authoritative read: the host-wide ContainerList snapshot
// can be a moment stale, so a container that entered backoff between the list
// and the inspect still shows State "running" in the list. State.Restarting
// from the inspect must catch it.
func TestEnsureCrewRuntime_RestartingDetectedFromInspectWhenListIsStale(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateRunning)),
		inspectBody: covRestartingInspect(covRuntimeRef),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id == "old-cid" {
		t.Fatalf("id = old-cid: the list said running, the inspect said restarting, and the stale list won")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, d := range f.deletes {
		if d == "old-cid" {
			found = true
		}
	}
	if !found {
		t.Errorf("restarting container was not removed, deletes = %v", f.deletes)
	}
}

// The guard must not widen: a genuinely running container is still reused
// without a create, which is the warm path every DAG wave depends on.
func TestEnsureCrewRuntime_RunningContainerStillReused(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	covCrewBindDirs(t, cfg)

	f := &covRT{
		listBody:    covExistingList(string(container.StateRunning)),
		inspectBody: covHealthyInspect(covRuntimeRef),
	}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid (running container reused)", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 0 {
		t.Errorf("running container must be reused, got %d creates", len(f.creates))
	}
	if len(f.deletes) != 0 {
		t.Errorf("running container must not be torn down, deletes = %v", f.deletes)
	}
}
