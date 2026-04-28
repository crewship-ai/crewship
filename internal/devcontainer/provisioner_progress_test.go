package devcontainer

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// TestProvisioner_Progress_RealDocker verifies that WithProgress fires a
// monotonically-increasing sequence of callbacks for a minimal real provision
// against a live Docker daemon. Skipped when no daemon is reachable, so unit
// CI on machines without Docker stays green; nightly e2e workflow exercises
// this against ubuntu-latest.
//
// "Real" means no mocks: we actually pull the base image, install one
// feature, and commit. The point is to catch breakage where the callback
// signature is right but plumbing (option pattern, total counter, message
// strings) drifts from what the API surface expects.
//
// Cap: 180 s. A first-time cold pull of devcontainers/base + common-utils on
// a fast network finishes in ~60-90 s; we double it as headroom.
func TestProvisioner_Progress_RealDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Docker provisioner test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	if _, err := docker.Ping(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	defer func() { _ = docker.Close() }()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cacheDir := t.TempDir()
	dl := NewFeatureDownloader(cacheDir, logger)
	installer := NewInstaller(docker, logger)
	p := NewProvisioner(docker, installer, dl, logger)

	cfg := &Config{
		Image: "mcr.microsoft.com/devcontainers/base:bookworm",
		Features: map[string]map[string]any{
			"ghcr.io/devcontainers/features/common-utils:2": {
				"installZsh": false,
				"installOhMyZsh": false,
			},
		},
	}

	type evt struct {
		step    int
		total   int
		message string
	}
	var (
		mu     sync.Mutex
		events []evt
	)
	cb := func(step, total int, message string) {
		mu.Lock()
		events = append(events, evt{step, total, message})
		mu.Unlock()
	}

	result, err := p.Provision(ctx, cfg.Image, cfg, "", WithProgress(cb))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Best-effort cleanup: drop the cache image so we don't leak across runs.
	defer func() {
		if result != nil && result.CachedImage != "" {
			_, _ = docker.ImageRemove(context.Background(), result.CachedImage, image.RemoveOptions{Force: true, PruneChildren: true})
		}
	}()

	mu.Lock()
	defer mu.Unlock()

	if len(events) < 3 {
		t.Fatalf("expected >=3 progress events (pull + feature + commit), got %d: %#v", len(events), events)
	}

	// Total must be stable across the run. Equal-or-decrease is a real bug
	// (UI progress bar would jump backwards).
	expectedTotal := events[len(events)-1].total
	for i, e := range events {
		if e.total != expectedTotal {
			t.Errorf("event[%d] total=%d, expected stable %d", i, e.total, expectedTotal)
		}
		if e.step < 1 || e.step > e.total {
			t.Errorf("event[%d] step=%d out of range 1..%d", i, e.step, e.total)
		}
		if e.message == "" {
			t.Errorf("event[%d] empty message", i)
		}
	}

	// Steps must be monotone non-decreasing. We don't require strict
	// increments because multiple features sharing an emit slot is fine,
	// but going BACKWARDS would break the UI.
	for i := 1; i < len(events); i++ {
		if events[i].step < events[i-1].step {
			t.Errorf("step regressed at i=%d: %d → %d", i, events[i-1].step, events[i].step)
		}
	}

	// Final event must reach total — otherwise the UI is stuck mid-progress.
	last := events[len(events)-1]
	if last.step != last.total {
		t.Errorf("last event step=%d, expected to equal total=%d", last.step, last.total)
	}

	// First event must be the base-image pull (we always emit that one).
	if events[0].step != 1 {
		t.Errorf("first event step=%d, expected 1 (pull)", events[0].step)
	}
}

// TestProvisioner_Progress_NoCustomizations exercises the "skip path" in
// Provision (no features, no postCreate, no containerEnv, no mise) which
// emits exactly one summary event. This proves the callback wiring works
// without standing up a full mock Docker stack — refactors of the option
// pattern would still break this test, but it doesn't need a daemon.
func TestProvisioner_Progress_NoCustomizations(t *testing.T) {
	mock := &mockCommitClient{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// installer/downloader can be nil — they're only used past the skip
	// check, which we deliberately don't reach.
	p := NewProvisioner(mock, nil, nil, logger)

	cfg := &Config{Image: "ubuntu:22.04"}

	var captured []struct{ step, total int }
	cb := func(step, total int, message string) {
		if message == "" {
			t.Error("empty progress message")
		}
		captured = append(captured, struct{ step, total int }{step, total})
	}

	if _, err := p.Provision(context.Background(), cfg.Image, cfg, "", WithProgress(cb)); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("expected exactly 1 progress event for empty config, got %d", len(captured))
	}
	if captured[0].step != 1 || captured[0].total != 1 {
		t.Errorf("expected (1,1) for skip path, got (%d,%d)", captured[0].step, captured[0].total)
	}
}
