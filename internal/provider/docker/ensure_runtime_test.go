package docker

// Tests for EnsureCrewRuntime that lock in the no-Docker-daemon
// invariants: resource-limit fallbacks, image-selection chain, and the
// CREWSHIP_RUNTIME env override.
//
// We do NOT touch a real Docker daemon. Instead, every test wires the
// Provider to an httptest server (newFakeDockerProvider, declared in
// fakeapi_test.go) and inspects the request body that the SDK sends to
// POST /containers/create. The fake handler also covers the auxiliary
// calls EnsureCrewRuntime makes on the happy path (list containers,
// image inspect, volume create, init-container chown, container start,
// post-start exec hooks) so the function reaches the ContainerCreate
// step without spurious errors.
//
// Style follows internal/provider/docker/fakeapi_test.go and the
// fixture-driven approach used in internal/api/*_test.go (subtests +
// t.Cleanup, no globals, no sleeps).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// dockerCreateCapture records every POST /containers/create request body
// the SDK sends. The "real" crew container is the one whose Config.User
// is "1001:1001" (the agent user); the init container that fixes
// bind-mount ownership uses "0:0" and we ignore it for our assertions
// (it's an implementation detail of the non-root host path).
type dockerCreateCapture struct {
	mu       sync.Mutex
	requests []container.CreateRequest
}

func (c *dockerCreateCapture) add(req container.CreateRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
}

// realCrew returns the first captured request whose Config.User matches
// the agent UID. Returns nil if none was seen (test will fail loudly).
func (c *dockerCreateCapture) realCrew() *container.CreateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.requests {
		if c.requests[i].Config != nil && c.requests[i].Config.User == "1001:1001" {
			return &c.requests[i]
		}
	}
	return nil
}

// newEnsureRuntimeFixture sets up a Provider + fake docker daemon that
// answers every API call EnsureCrewRuntime makes on the happy path. The
// returned capture is populated with each ContainerCreate request the
// SDK emits.
//
// The fixture uses t.TempDir() for OutputBasePath so the host-side
// MkdirAll calls succeed; chown will fail as non-root, exercising the
// init-container fallback. That's fine — the fake handler accepts both
// creates.
func newEnsureRuntimeFixture(t *testing.T, cfg Config) (*Provider, *dockerCreateCapture) {
	t.Helper()

	if cfg.OutputBasePath == "" {
		cfg.OutputBasePath = t.TempDir()
	}
	if cfg.SidecarBinaryPath == "" {
		cfg.SidecarBinaryPath = "/fake/sidecar"
	}
	if cfg.EntrypointPath == "" {
		cfg.EntrypointPath = "/fake/entrypoint.sh"
	}

	capture := &dockerCreateCapture{}

	// One handler dispatching by suffix — Docker URLs include an API
	// version prefix (/v1.43/...) so http.ServeMux exact matches break.
	handler := func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		// GET /v*/containers/json — list. Empty so the create path runs.
		case strings.HasSuffix(path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))

		// POST /v*/containers/create — capture, synthetic ID.
		case strings.HasSuffix(path, "/containers/create"):
			var req container.CreateRequest
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			capture.add(req)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "fake-cid-0123456789ab"})

		// GET /v*/volumes — list. Empty so the legacy-C1 migration finds
		// nothing to migrate and provisioning proceeds (it now fails closed
		// when it can't enumerate volumes).
		case strings.HasSuffix(path, "/volumes") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": []any{}, "Warnings": nil})

		// POST /v*/volumes/create — accept.
		case strings.HasSuffix(path, "/volumes/create"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "fake-volume"})

		// GET /v*/networks — list. Pretend the configured network
		// already exists so we never POST /networks/create.
		case strings.HasSuffix(path, "/networks"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Name": cfg.Network, "Id": "net-existing"},
			})

		// GET /v*/images/{ref}/json — ImageInspect. Minimal valid body
		// so localPresent=true and ensureImage short-circuits without
		// pulling.
		case strings.HasSuffix(path, "/json") && strings.Contains(path, "/images/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:fakeimg",
				"RepoDigests": []string{},
				"Config":      map[string]any{"Env": []string{}},
			})

		// Container start: 204 no content.
		case strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)

		// Container wait (used by init-container chown path).
		case strings.HasSuffix(path, "/wait"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 0})

		// Exec create — return synthetic exec ID.
		case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "fake-exec-id"})

		// Exec inspect — return "not running, exit 0" so the polling
		// loop in EnsureCrewRuntime's post-start exits immediately.
		case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Running":  false,
				"ExitCode": 0,
			})

		// DELETE /v*/containers/{id} — used to clean up init container.
		case strings.Contains(path, "/containers/") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)

		default:
			// Everything else (exec start, /_ping, digest manifest
			// probes, etc.) — 200 OK is harmless. Test assertions check
			// the captured create body, not request counts here.
			w.WriteHeader(http.StatusOK)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))

	cli, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		client.WithVersion("1.43"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("docker client: %v", err)
	}

	p := &Provider{
		client:         cli,
		cfg:            cfg,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		digestResolver: dockerutil.NewDigestResolver(0, 0),
	}

	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})

	return p, capture
}

// TestDockerProvider_MemoryFallback_ZeroDefaultsTo8GiB locks the
// regression fixed in PR #389: a CrewConfig with MemoryMB=0 (the Go
// zero value, frequently emitted when chatbridge.resolver fails to load
// the crew row) must NOT pass 0 to Docker. Instead the provider clamps
// it to 8192 MiB, matching the comment at docker_container.go:160-170.
// 512 MiB previously caused exit-137 OOM kills under normal agent load.
func TestDockerProvider_MemoryFallback_ZeroDefaultsTo8GiB(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:       "crew-1",
		Slug:     "eng",
		MemoryMB: 0,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user (1001:1001) container create captured")
	}
	const want = int64(8192) * 1024 * 1024
	if req.HostConfig.Resources.Memory != want {
		t.Errorf("Memory = %d bytes, want %d (8 GiB default)",
			req.HostConfig.Resources.Memory, want)
	}
}

// TestDockerProvider_MemoryFallback_NegativeDefaultsTo8GiB covers the
// explicit "<= 0" guard that protects against a stray sentinel value
// (e.g. -1 to mean "unset"). The Docker daemon would reject a negative
// Memory outright; the provider must never let that hit the wire.
func TestDockerProvider_MemoryFallback_NegativeDefaultsTo8GiB(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:       "crew-neg",
		Slug:     "eng",
		MemoryMB: -1,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	const want = int64(8192) * 1024 * 1024
	if req.HostConfig.Resources.Memory != want {
		t.Errorf("Memory = %d bytes, want %d (8 GiB default for negative MemoryMB)",
			req.HostConfig.Resources.Memory, want)
	}
}

// TestDockerProvider_MemoryFallback_ExplicitValuePassedThrough confirms
// the fallback only fires for non-positive values — a real, positive
// MemoryMB from the crew row must be honoured exactly.
func TestDockerProvider_MemoryFallback_ExplicitValuePassedThrough(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:       "crew-explicit",
		Slug:     "eng",
		MemoryMB: 4096,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	const want = int64(4096) * 1024 * 1024
	if req.HostConfig.Resources.Memory != want {
		t.Errorf("Memory = %d bytes, want %d (explicit 4096 MiB)",
			req.HostConfig.Resources.Memory, want)
	}
}

// TestDockerProvider_CPUFallback_ZeroDefaultsTo2 mirrors the memory
// fallback for CPUs: zero (Go default for float64) -> 2.0 cores. Same
// rationale as Memory — a 0-CPU container is unschedulable.
func TestDockerProvider_CPUFallback_ZeroDefaultsTo2(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-cpu0",
		Slug: "eng",
		CPUs: 0,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	const want = int64(2.0 * 1e9)
	if req.HostConfig.Resources.NanoCPUs != want {
		t.Errorf("NanoCPUs = %d, want %d (2.0 cores default)",
			req.HostConfig.Resources.NanoCPUs, want)
	}
}

// TestDockerProvider_CPUFallback_NegativeDefaultsTo2 — same guard for
// negative sentinel values.
func TestDockerProvider_CPUFallback_NegativeDefaultsTo2(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-cpuneg",
		Slug: "eng",
		CPUs: -1,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	const want = int64(2.0 * 1e9)
	if req.HostConfig.Resources.NanoCPUs != want {
		t.Errorf("NanoCPUs = %d, want %d (2.0 cores default for negative CPUs)",
			req.HostConfig.Resources.NanoCPUs, want)
	}
}

// TestDockerProvider_CPUFallback_ExplicitValuePassedThrough — a real,
// positive CPU count is honoured exactly (4 cores → 4e9 NanoCPUs).
func TestDockerProvider_CPUFallback_ExplicitValuePassedThrough(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-cpu4",
		Slug: "eng",
		CPUs: 4,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	const want = int64(4 * 1e9)
	if req.HostConfig.Resources.NanoCPUs != want {
		t.Errorf("NanoCPUs = %d, want %d (explicit 4 cores)",
			req.HostConfig.Resources.NanoCPUs, want)
	}
}

// TestDockerProvider_ImageSelection_RuntimeImageWhenNeitherOverride
// — with no team.Image and no team.CachedImage, the provider uses the
// configured RuntimeImage. Baseline behaviour.
func TestDockerProvider_ImageSelection_RuntimeImageWhenNeitherOverride(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "default/runtime:v1"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-img-default",
		Slug: "eng",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	if req.Config.Image != "default/runtime:v1" {
		t.Errorf("Image = %q, want %q (RuntimeImage)",
			req.Config.Image, "default/runtime:v1")
	}
}

// TestDockerProvider_ImageSelection_TeamImageOverridesRuntimeImage —
// when the crew config sets Image, the provider uses it instead of the
// global RuntimeImage. Custom devcontainer image path.
func TestDockerProvider_ImageSelection_TeamImageOverridesRuntimeImage(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "default/runtime:v1"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:    "crew-img-team",
		Slug:  "eng",
		Image: "custom/dev:latest",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	if req.Config.Image != "custom/dev:latest" {
		t.Errorf("Image = %q, want %q (team.Image override)",
			req.Config.Image, "custom/dev:latest")
	}
}

// TestDockerProvider_ImageSelection_CachedImageWinsOverBoth — when the
// devcontainer provisioner has produced a cached image, CachedImage
// must win over both Image and RuntimeImage. Locks the priority chain
// at docker_container.go:176-183.
func TestDockerProvider_ImageSelection_CachedImageWinsOverBoth(t *testing.T) {
	t.Parallel()

	p, cap := newEnsureRuntimeFixture(t, Config{RuntimeImage: "default/runtime:v1"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:          "crew-img-cached",
		Slug:        "eng",
		Image:       "custom/dev:latest",
		CachedImage: "crewship/cache:crew-1-abc",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	if req.Config.Image != "crewship/cache:crew-1-abc" {
		t.Errorf("Image = %q, want %q (CachedImage wins)",
			req.Config.Image, "crewship/cache:crew-1-abc")
	}
}

// TestDockerProvider_RuntimeOverride_EnvWins — CREWSHIP_RUNTIME env var
// overrides cfg.DefaultRuntime. Used by operators on hosts that have
// gVisor / Kata / sysbox installed without rebuilding the binary.
func TestDockerProvider_RuntimeOverride_EnvWins(t *testing.T) {
	// NOT t.Parallel(): t.Setenv mutates process env.
	t.Setenv("CREWSHIP_RUNTIME", "runsc")

	p, cap := newEnsureRuntimeFixture(t, Config{
		RuntimeImage:   "fake/runtime:latest",
		DefaultRuntime: "runc",
	})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-runtime",
		Slug: "eng",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	if req.HostConfig.Runtime != "runsc" {
		t.Errorf("Runtime = %q, want %q (CREWSHIP_RUNTIME wins over DefaultRuntime=runc)",
			req.HostConfig.Runtime, "runsc")
	}
}

// TestDockerProvider_RuntimeOverride_DefaultRunc — when neither
// CREWSHIP_RUNTIME nor cfg.DefaultRuntime is set, the provider falls
// back to "runc". The default fallback at docker_container.go:152-154.
func TestDockerProvider_RuntimeOverride_DefaultRunc(t *testing.T) {
	// NOT t.Parallel(): we need to be sure CREWSHIP_RUNTIME is unset.
	t.Setenv("CREWSHIP_RUNTIME", "")

	p, cap := newEnsureRuntimeFixture(t, Config{
		RuntimeImage: "fake/runtime:latest",
		// DefaultRuntime intentionally empty.
	})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-runtime-def",
		Slug: "eng",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	if req.HostConfig.Runtime != "runc" {
		t.Errorf("Runtime = %q, want \"runc\" (default fallback)",
			req.HostConfig.Runtime)
	}
}

// TestDockerProvider_RuntimeOverride_ConfigUsedWhenEnvUnset confirms
// the env var is opt-in: when it's unset, cfg.DefaultRuntime takes
// effect. Order at docker_container.go:152-158:
//
//  1. cfg.DefaultRuntime (or "runc" if empty)
//  2. CREWSHIP_RUNTIME env (if non-empty)
func TestDockerProvider_RuntimeOverride_ConfigUsedWhenEnvUnset(t *testing.T) {
	// NOT t.Parallel(): mutates env.
	t.Setenv("CREWSHIP_RUNTIME", "")

	p, cap := newEnsureRuntimeFixture(t, Config{
		RuntimeImage:   "fake/runtime:latest",
		DefaultRuntime: "sysbox-runc",
	})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-runtime-cfg",
		Slug: "eng",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := cap.realCrew()
	if req == nil {
		t.Fatal("no agent-user container create captured")
	}
	if req.HostConfig.Runtime != "sysbox-runc" {
		t.Errorf("Runtime = %q, want %q (cfg.DefaultRuntime when env unset)",
			req.HostConfig.Runtime, "sysbox-runc")
	}
}
