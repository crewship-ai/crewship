package docker

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEnsureCrewRuntime_WarmCacheSkipsSecondList is the #836 regression
// guard: once a crew's container is confirmed running, a follow-up
// EnsureCrewRuntime within warmCrewTTL (a DAG wave's sibling steps, or a
// prewarm + first step) must NOT re-scan every container on the host.
func TestEnsureCrewRuntime_WarmCacheSkipsSecondList(t *testing.T) {
	t.Parallel()

	inspect, _ := json.Marshal(map[string]any{
		"Id":     "old-cid",
		"State":  map[string]any{"Running": true},
		"Config": map[string]any{"Image": covRuntimeRef},
		"Mounts": []map[string]any{
			{"Destination": "/crew"},
			{"Destination": "/home/agent"},
			{"Destination": "/opt/crew-tools"},
		},
	})
	f := &covRT{listBody: covExistingList("running"), inspectBody: string(inspect)}
	p := f.provider(t, covRTConfig(t))

	// First call: cold path lists the host, reuses the running container,
	// and warms the cache.
	id1, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("first EnsureCrewRuntime: %v", err)
	}
	f.mu.Lock()
	firstList := f.listCount
	f.mu.Unlock()
	if firstList == 0 {
		t.Fatal("first call should have performed a container list")
	}

	// Second call within TTL: warm hit — must return the same id without
	// another host-wide list.
	id2, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("second EnsureCrewRuntime: %v", err)
	}
	if id1 != id2 {
		t.Errorf("warm hit returned a different id: %q vs %q", id1, id2)
	}
	f.mu.Lock()
	secondList := f.listCount
	f.mu.Unlock()
	if secondList != firstList {
		t.Errorf("warm hit must skip ContainerList; list count went %d → %d", firstList, secondList)
	}
}

// TestEnsureCrewRuntime_WarmCacheEvictedOnRecreate: tearing a container
// down (here: a missing required mount forces recreate) must evict the
// cache so the next call re-reconciles instead of returning a dead id.
func TestEnsureCrewRuntime_WarmCacheEvictedOnRecreate(t *testing.T) {
	t.Parallel()

	// Missing /opt/crew-tools mount → recreate path → evictWarm.
	inspect, _ := json.Marshal(map[string]any{
		"Id":     "old-cid",
		"State":  map[string]any{"Running": true},
		"Config": map[string]any{"Image": covRuntimeRef},
		"Mounts": []map[string]any{{"Destination": "/crew"}, {"Destination": "/home/agent"}},
	})
	f := &covRT{listBody: covExistingList("running"), inspectBody: string(inspect)}
	p := f.provider(t, covRTConfig(t))

	newID, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	// The stale "old-cid" was evicted; the cache must now point at the
	// freshly created container, never the torn-down one.
	if newID == "old-cid" {
		t.Fatal("expected a freshly created container, got the stale one")
	}
	got, ok := p.warmHit(covTeam().ID)
	if !ok {
		t.Fatal("create path should warm the cache")
	}
	if got == "old-cid" {
		t.Errorf("warm cache still holds the torn-down container id")
	}
	if got != newID {
		t.Errorf("warm cache = %q, want the new container %q", got, newID)
	}
}
