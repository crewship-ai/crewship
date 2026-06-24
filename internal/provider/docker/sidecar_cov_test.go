package docker

// Coverage tests for sidecar.go — the per-crew service (Redis/Postgres/
// etc.) lifecycle. No real Docker daemon: every test wires the Provider
// to an httptest server that fakes the slice of the Docker REST API the
// code under test touches (same approach as fakeapi_test.go).

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
	"time"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// newCovProvider wires a Provider to an httptest fake docker daemon with
// a silent logger and a digest resolver that fails fast (no real network:
// all image refs used by these tests point at 127.0.0.1, so registry
// HEADs are instant connection-refused or served by the same fake).
func newCovProvider(t *testing.T, cfg Config, handler http.HandlerFunc) *Provider {
	t.Helper()

	srv := httptest.NewServer(handler)
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
		digestResolver: dockerutil.NewDigestResolver(time.Hour, 2*time.Second),
		// Default the test provider to host-local-daemon semantics so volume
		// self-heal tests run their intended path regardless of the test
		// host's OS. The VM-runtime case is covered by an explicit test that
		// flips this to false.
		checkVolumeMountpoint: true,
	}
	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})
	return p
}

// newCovProviderTCP is newCovProvider with a tcp:// docker host. The
// SDK's hijack path (exec attach) dials the raw network named in the
// host URL, so http:// hosts break it with "unknown network http" —
// tests that exercise hijacked connections must use this variant.
func newCovProviderTCP(t *testing.T, cfg Config, handler http.HandlerFunc) *Provider {
	t.Helper()

	srv := httptest.NewServer(handler)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")),
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
		digestResolver: dockerutil.NewDigestResolver(time.Hour, 2*time.Second),
		// Default the test provider to host-local-daemon semantics so volume
		// self-heal tests run their intended path regardless of the test
		// host's OS. The VM-runtime case is covered by an explicit test that
		// flips this to false.
		checkVolumeMountpoint: true,
	}
	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})
	return p
}

func covRedisSvc() provider.CrewService {
	return provider.CrewService{
		Name:    "redis",
		Image:   "redis:7",
		Command: []string{"redis-server", "--appendonly", "yes"},
		Env:     map[string]string{"B": "2", "A": "1"},
		Ports:   []string{"6379", "5432/tcp"},
		Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data"}},
	}
}

func TestReadToDiscard_DrainsAll(t *testing.T) {
	t.Parallel()
	n, err := readToDiscard(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("readToDiscard: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
}

func TestVolumeListOptions_NoFilters(t *testing.T) {
	t.Parallel()
	opts := volumeListOptions()
	if opts.Filters.Len() != 0 {
		t.Errorf("expected no filters, got %d", opts.Filters.Len())
	}
}

func TestSidecarContainerName(t *testing.T) {
	t.Parallel()
	p := &Provider{cfg: Config{}}
	if got := p.sidecarContainerName("alpha", "redis"); got != "crewship-svc-alpha-redis" {
		t.Errorf("default prefix name = %q", got)
	}
	p.cfg.ContainerPrefix = "acme"
	if got := p.sidecarContainerName("alpha", "redis"); got != "acme-svc-alpha-redis" {
		t.Errorf("custom prefix name = %q", got)
	}
}

func TestSidecarVolumeName(t *testing.T) {
	t.Parallel()
	p := &Provider{cfg: Config{}}
	if got := p.sidecarVolumeName("alpha", "data"); got != "crewship-svc-alpha-vol-data" {
		t.Errorf("default prefix volume = %q", got)
	}
	p.cfg.ContainerPrefix = "acme"
	if got := p.sidecarVolumeName("alpha", "data"); got != "acme-svc-alpha-vol-data" {
		t.Errorf("custom prefix volume = %q", got)
	}
}

func TestEnsureCrewServices_NoServicesIsNoop(t *testing.T) {
	t.Parallel()

	var calls int32
	var mu sync.Mutex
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	ids, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{ID: "c1", Slug: "alpha"})
	if err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("expected zero docker API calls, got %d", calls)
	}
}

func TestEnsureCrewServices_RequiresSlug(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	})

	_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID:       "c1",
		Services: []provider.CrewService{covRedisSvc()},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a crew slug") {
		t.Fatalf("expected slug error, got %v", err)
	}
}

func TestEnsureCrewServices_NetworkEnsureError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{Network: "covnet"}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"daemon down"}`, http.StatusInternalServerError)
	})

	_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: "c1", Slug: "alpha",
		Services: []provider.CrewService{covRedisSvc()},
	})
	if err == nil || !strings.Contains(err.Error(), "ensure network for services") {
		t.Fatalf("expected ensure-network error, got %v", err)
	}
}

func TestEnsureCrewServices_ReusesMatchingRunningSidecar(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	hash := computeSidecarSpecHash(&svc)

	creates := 0
	var mu sync.Mutex
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":    "cid-redis",
				"Names": []string{"/crewship-svc-alpha-redis"},
				"Image": "redis:7",
				"State": "running",
				"Labels": map[string]string{
					sidecarSpecHashLabel: hash,
				},
			}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates++
			http.Error(w, `{"message":"should not create"}`, http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	ids, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: "c1", Slug: "alpha",
		Services: []provider.CrewService{svc},
	})
	if err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}
	if ids["redis"] != "cid-redis" {
		t.Errorf("ids = %v, want redis=cid-redis", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 0 {
		t.Errorf("matching running sidecar must be reused, got %d creates", creates)
	}
}

func TestEnsureCrewServices_StartsStoppedMatchingSidecar(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	hash := computeSidecarSpecHash(&svc)

	var starts []string
	var mu sync.Mutex
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":     "cid-redis",
				"Names":  []string{"/crewship-svc-alpha-redis"},
				"Image":  "redis:7",
				"State":  "exited",
				"Labels": map[string]string{sidecarSpecHashLabel: hash},
			}})
		case strings.HasSuffix(r.URL.Path, "/start"):
			starts = append(starts, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	ids, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: "c1", Slug: "alpha",
		Services: []provider.CrewService{svc},
	})
	if err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}
	if ids["redis"] != "cid-redis" {
		t.Errorf("ids = %v", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 1 || !strings.Contains(starts[0], "cid-redis") {
		t.Errorf("expected one start for cid-redis, got %v", starts)
	}
}

func TestEnsureSidecar_StartExistingError(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	hash := computeSidecarSpecHash(&svc)

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":     "cid-redis",
				"Names":  []string{"/crewship-svc-alpha-redis"},
				"Image":  "redis:7",
				"State":  "exited",
				"Labels": map[string]string{sidecarSpecHashLabel: hash},
			}})
		case strings.HasSuffix(r.URL.Path, "/start"):
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), "start existing sidecar") {
		t.Fatalf("expected start-existing error, got %v", err)
	}
}

func TestEnsureSidecar_ListError(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), "list containers") {
		t.Fatalf("expected list error, got %v", err)
	}
}

// TestEnsureSidecar_RecreateOnSpecDrift pins the drift path: image matches
// but the stored spec hash differs → stop (tolerating a stop failure) +
// force-remove + recreate.
func TestEnsureSidecar_RecreateOnSpecDrift(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()

	var mu sync.Mutex
	var removed, created, started bool
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":     "cid-old",
				"Names":  []string{"/crewship-svc-alpha-redis"},
				"Image":  "redis:7",
				"State":  "running",
				"Labels": map[string]string{sidecarSpecHashLabel: "stale-hash"},
			}})
		case strings.HasSuffix(path, "/stop"):
			// Stop failure must be tolerated (container may already be gone).
			http.Error(w, `{"message":"already stopped"}`, http.StatusInternalServerError)
		case r.Method == http.MethodDelete && strings.Contains(path, "/containers/cid-old"):
			removed = true
			if r.URL.Query().Get("force") != "1" {
				t.Errorf("remove should be forced, query=%v", r.URL.Query())
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodGet && strings.Contains(path, "/volumes/"):
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/volumes/create"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v"})
		case strings.HasSuffix(path, "/containers/create"):
			created = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid-new"})
		case strings.HasSuffix(path, "/start"):
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	id, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err != nil {
		t.Fatalf("ensureSidecar: %v", err)
	}
	if id != "cid-new" {
		t.Errorf("id = %q, want cid-new", id)
	}
	mu.Lock()
	defer mu.Unlock()
	if !removed || !created || !started {
		t.Errorf("removed=%v created=%v started=%v, want all true", removed, created, started)
	}
}

func TestEnsureSidecar_RecreateOnImageDrift_RemoveFails(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc() // wants redis:7; existing runs redis:6
	hash := computeSidecarSpecHash(&svc)

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":     "cid-old",
				"Names":  []string{"/crewship-svc-alpha-redis"},
				"Image":  "redis:6",
				"State":  "running",
				"Labels": map[string]string{sidecarSpecHashLabel: hash},
			}})
		case strings.HasSuffix(r.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			http.Error(w, `{"message":"device busy"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), "remove sidecar") {
		t.Fatalf("expected remove error, got %v", err)
	}
}

// TestEnsureSidecar_CreateBody drives the full create path (no existing
// container) and asserts the exact docker create request: name, image,
// env, command, exposed ports, labels (incl. spec hash), healthcheck,
// hardening (no-new-privileges + pids limit), volume mounts and the
// network alias.
func TestEnsureSidecar_CreateBody(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	svc.Healthcheck = &provider.CrewServiceHealthcheck{
		Test:        []string{"CMD", "redis-cli", "ping"},
		Interval:    2 * time.Second,
		Timeout:     time.Second,
		Retries:     3,
		StartPeriod: 5 * time.Second,
	}
	wantHash := computeSidecarSpecHash(&svc)

	var mu sync.Mutex
	var createReq container.CreateRequest
	var createRawNetworking struct {
		NetworkingConfig struct {
			EndpointsConfig map[string]struct {
				Aliases []string
			}
		}
	}
	var createName string
	var volumeCreates []string
	p := newCovProvider(t, Config{Network: "covnet"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodGet && strings.Contains(path, "/volumes/"):
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/volumes/create"):
			var vreq struct{ Name string }
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &vreq)
			volumeCreates = append(volumeCreates, vreq.Name)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": vreq.Name})
		case strings.HasSuffix(path, "/containers/create"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &createReq)
			_ = json.Unmarshal(body, &createRawNetworking)
			createName = r.URL.Query().Get("name")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid-new"})
		case strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	id, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err != nil {
		t.Fatalf("ensureSidecar: %v", err)
	}
	if id != "cid-new" {
		t.Errorf("id = %q", id)
	}

	mu.Lock()
	defer mu.Unlock()

	if createName != "crewship-svc-alpha-redis" {
		t.Errorf("create name = %q", createName)
	}
	cfg := createReq.Config
	if cfg == nil {
		t.Fatal("no Config captured")
	}
	if cfg.Image != "redis:7" {
		t.Errorf("image = %q", cfg.Image)
	}
	envSet := map[string]bool{}
	for _, e := range cfg.Env {
		envSet[e] = true
	}
	if !envSet["A=1"] || !envSet["B=2"] {
		t.Errorf("env = %v, want A=1 and B=2", cfg.Env)
	}
	if strings.Join(cfg.Cmd, " ") != "redis-server --appendonly yes" {
		t.Errorf("cmd = %v", cfg.Cmd)
	}
	if _, ok := cfg.ExposedPorts["6379/tcp"]; !ok {
		t.Errorf("exposed ports missing 6379/tcp: %v", cfg.ExposedPorts)
	}
	if _, ok := cfg.ExposedPorts["5432/tcp"]; !ok {
		t.Errorf("exposed ports missing 5432/tcp: %v", cfg.ExposedPorts)
	}
	wantLabels := map[string]string{
		"managed-by":         "crewship",
		"crewship.crew":      "alpha",
		"crewship.kind":      "sidecar",
		"crewship.svc":       "redis",
		sidecarSpecHashLabel: wantHash,
	}
	for k, v := range wantLabels {
		if cfg.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, cfg.Labels[k], v)
		}
	}
	if cfg.Healthcheck == nil {
		t.Fatal("healthcheck not propagated")
	}
	if strings.Join(cfg.Healthcheck.Test, " ") != "CMD redis-cli ping" {
		t.Errorf("healthcheck test = %v", cfg.Healthcheck.Test)
	}
	if cfg.Healthcheck.Interval != 2*time.Second || cfg.Healthcheck.Retries != 3 {
		t.Errorf("healthcheck interval/retries = %v/%d", cfg.Healthcheck.Interval, cfg.Healthcheck.Retries)
	}

	hc := createReq.HostConfig
	if hc == nil {
		t.Fatal("no HostConfig captured")
	}
	if len(hc.SecurityOpt) != 1 || hc.SecurityOpt[0] != "no-new-privileges:true" {
		t.Errorf("SecurityOpt = %v", hc.SecurityOpt)
	}
	if hc.Resources.PidsLimit == nil || *hc.Resources.PidsLimit != 512 {
		t.Errorf("PidsLimit = %v, want 512", hc.Resources.PidsLimit)
	}
	if hc.RestartPolicy.Name != container.RestartPolicyOnFailure || hc.RestartPolicy.MaximumRetryCount != 3 {
		t.Errorf("RestartPolicy = %+v", hc.RestartPolicy)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Source != "crewship-svc-alpha-vol-data" || hc.Mounts[0].Target != "/data" {
		t.Errorf("Mounts = %+v", hc.Mounts)
	}

	ep, ok := createRawNetworking.NetworkingConfig.EndpointsConfig["covnet"]
	if !ok {
		t.Fatalf("networking config missing covnet endpoint: %+v", createRawNetworking)
	}
	if len(ep.Aliases) != 1 || ep.Aliases[0] != "redis" {
		t.Errorf("aliases = %v, want [redis]", ep.Aliases)
	}

	if len(volumeCreates) != 1 || volumeCreates[0] != "crewship-svc-alpha-vol-data" {
		t.Errorf("volume creates = %v", volumeCreates)
	}
}

func TestEnsureSidecar_CreateError(t *testing.T) {
	t.Parallel()

	svc := provider.CrewService{Name: "redis", Image: "redis:7"}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		case strings.HasSuffix(path, "/containers/create"):
			http.Error(w, `{"message":"conflict"}`, http.StatusConflict)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), "create sidecar") {
		t.Fatalf("expected create error, got %v", err)
	}
}

func TestEnsureSidecar_StartError(t *testing.T) {
	t.Parallel()

	svc := provider.CrewService{Name: "redis", Image: "redis:7"}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		case strings.HasSuffix(path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid-new"})
		case strings.HasSuffix(path, "/start"):
			http.Error(w, `{"message":"oci runtime error"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), "start sidecar") {
		t.Fatalf("expected start error, got %v", err)
	}
}

func TestEnsureSidecar_VolumeEnsureError(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodGet && strings.Contains(path, "/volumes/"):
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/volumes/create"):
			http.Error(w, `{"message":"disk full"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), `ensure volume "data"`) {
		t.Fatalf("expected ensure-volume error, got %v", err)
	}
}

func TestPullSidecarImage_LocalCopyFallbackOnPullError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:local"})
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"registry unreachable"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.pullSidecarImage(context.Background(), "redis:7"); err != nil {
		t.Fatalf("expected nil (local copy fallback), got %v", err)
	}
}

func TestPullSidecarImage_ErrorWhenNoLocalCopy(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"registry unreachable"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.pullSidecarImage(context.Background(), "redis:7")
	if err == nil || !strings.Contains(err.Error(), "pull redis:7") {
		t.Fatalf("expected pull error, got %v", err)
	}
}

func TestPullSidecarImage_SuccessDrainsStream(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte(`{"status":"Pulling from redis"}` + "\n"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.pullSidecarImage(context.Background(), "redis:7"); err != nil {
		t.Fatalf("pullSidecarImage: %v", err)
	}
}

func TestWaitSidecarHealthy_TimeoutBeforeFirstTick(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no inspect should happen before the first 1s tick")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err := p.waitSidecarHealthy(ctx, "cid")
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for healthy") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// TestWaitSidecarHealthy_TransientStatesThenRunning walks the poll loop
// through every "keep polling" branch: inspect error → nil State →
// running-but-no-health... wait, that returns. Sequence is:
// tick 1: inspect 500 (transient, continue)
// tick 2: State null (continue)
// tick 3: running with no healthcheck → ready (return nil)
func TestWaitSidecarHealthy_TransientStatesThenRunning(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	call := 0
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		call++
		n := call
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			http.Error(w, `{"message":"transient"}`, http.StatusInternalServerError)
		case 2:
			_, _ = w.Write([]byte(`{"Id":"cid","State":null}`))
		default:
			_, _ = w.Write([]byte(`{"Id":"cid","State":{"Running":true}}`))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.waitSidecarHealthy(ctx, "cid"); err != nil {
		t.Fatalf("waitSidecarHealthy: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if call != 3 {
		t.Errorf("expected 3 inspect calls, got %d", call)
	}
}

func TestWaitSidecarHealthy_NotRunningNoHealthKeepsPolling(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	call := 0
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		call++
		n := call
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// Created but not yet running, no healthcheck → keep polling.
			_, _ = w.Write([]byte(`{"Id":"cid","State":{"Running":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"Id":"cid","State":{"Running":true,"Health":{"Status":"healthy"}}}`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.waitSidecarHealthy(ctx, "cid"); err != nil {
		t.Fatalf("waitSidecarHealthy: %v", err)
	}
}

// TestEnsureCrewServices_HealthcheckGate covers the post-start health
// gate: a service that declares a healthcheck blocks EnsureCrewServices
// until docker reports healthy / unhealthy.
func TestEnsureCrewServices_HealthcheckGate(t *testing.T) {
	t.Parallel()

	mkSvc := func() provider.CrewService {
		svc := covRedisSvc()
		svc.Healthcheck = &provider.CrewServiceHealthcheck{Test: []string{"CMD", "redis-cli", "ping"}, Retries: 1}
		return svc
	}

	mkHandler := func(healthStatus string) http.HandlerFunc {
		svc := mkSvc()
		hash := computeSidecarSpecHash(&svc)
		return func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case strings.HasSuffix(path, "/containers/json"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"Id":     "cid-redis",
					"Names":  []string{"/crewship-svc-alpha-redis"},
					"Image":  "redis:7",
					"State":  "running",
					"Labels": map[string]string{sidecarSpecHashLabel: hash},
				}})
			case strings.Contains(path, "/containers/cid-redis/json"):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"Id":"cid-redis","State":{"Running":true,"Health":{"Status":"` + healthStatus + `"}}}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		}
	}

	t.Run("healthy passes", func(t *testing.T) {
		t.Parallel()
		p := newCovProvider(t, Config{}, mkHandler("healthy"))
		ids, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
			ID: "c1", Slug: "alpha", Services: []provider.CrewService{mkSvc()},
		})
		if err != nil {
			t.Fatalf("EnsureCrewServices: %v", err)
		}
		if ids["redis"] != "cid-redis" {
			t.Errorf("ids = %v", ids)
		}
	})

	t.Run("unhealthy fails loudly", func(t *testing.T) {
		t.Parallel()
		p := newCovProvider(t, Config{}, mkHandler("unhealthy"))
		_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
			ID: "c1", Slug: "alpha", Services: []provider.CrewService{mkSvc()},
		})
		if err == nil || !strings.Contains(err.Error(), `sidecar "redis" not healthy`) {
			t.Fatalf("expected not-healthy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "sidecar reported unhealthy") {
			t.Errorf("error should carry the unhealthy cause: %v", err)
		}
	})
}

func TestEnsureCrewServices_SidecarErrorIsNamed(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
	})

	_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: "c1", Slug: "alpha",
		Services: []provider.CrewService{{Name: "pg", Image: "postgres:16"}},
	})
	if err == nil || !strings.Contains(err.Error(), `sidecar "pg"`) {
		t.Fatalf("expected error naming the sidecar, got %v", err)
	}
}

func covServiceListJSON() []map[string]any {
	return []map[string]any{
		{"Id": "c1", "State": "running", "Labels": map[string]string{"crewship.crew": "alpha", "crewship.kind": "sidecar"}},
		{"Id": "c2", "State": "exited", "Labels": map[string]string{"crewship.crew": "alpha", "crewship.kind": "sidecar"}},
		{"Id": "c3", "State": "running", "Labels": map[string]string{"crewship.crew": "beta", "crewship.kind": "sidecar"}},
		{"Id": "c4", "State": "running", "Labels": map[string]string{"crewship.crew": "alpha", "crewship.kind": "runtime"}},
	}
}

func TestStopCrewServices_StopsOnlyRunningCrewSidecars(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var stopped []string
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(covServiceListJSON())
		case strings.HasSuffix(r.URL.Path, "/stop"):
			parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/stop"), "/")
			stopped = append(stopped, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	if err := p.StopCrewServices(context.Background(), "alpha"); err != nil {
		t.Fatalf("StopCrewServices: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stopped) != 1 || stopped[0] != "c1" {
		t.Errorf("stopped = %v, want only c1 (running alpha sidecar)", stopped)
	}
}

func TestStopCrewServices_AggregatesFailures(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(covServiceListJSON())
		case strings.HasSuffix(r.URL.Path, "/stop"):
			http.Error(w, `{"message":"cannot stop"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.StopCrewServices(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), `stop 1 sidecar(s) for crew "alpha"`) {
		t.Fatalf("expected aggregated stop error, got %v", err)
	}
}

func TestStopCrewServices_ListError(t *testing.T) {
	t.Parallel()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
	})
	err := p.StopCrewServices(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "list containers") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestRemoveCrewServices_RemovesAllCrewSidecarsRegardlessOfState(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var removed []string
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(covServiceListJSON())
		case r.Method == http.MethodDelete:
			parts := strings.Split(r.URL.Path, "/")
			removed = append(removed, parts[len(parts)-1])
			if r.URL.Query().Get("force") != "1" {
				t.Errorf("remove should be forced")
			}
			if r.URL.Query().Get("v") == "1" {
				t.Errorf("remove must NOT delete volumes")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	if err := p.RemoveCrewServices(context.Background(), "alpha"); err != nil {
		t.Fatalf("RemoveCrewServices: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"c1": true, "c2": true}
	if len(removed) != 2 || !want[removed[0]] || !want[removed[1]] {
		t.Errorf("removed = %v, want c1+c2", removed)
	}
}

func TestRemoveCrewServices_AggregatesFailures(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(covServiceListJSON())
		case r.Method == http.MethodDelete:
			http.Error(w, `{"message":"busy"}`, http.StatusConflict)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.RemoveCrewServices(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), `remove 2 sidecar(s) for crew "alpha"`) {
		t.Fatalf("expected aggregated remove error, got %v", err)
	}
}

func TestRemoveCrewServices_ListError(t *testing.T) {
	t.Parallel()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
	})
	err := p.RemoveCrewServices(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "list containers") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestRemoveCrewServiceVolumes_RemovesOnlyCrewPrefixedVolumes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var removed []string
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes"):
			w.Header().Set("Content-Type", "application/json")
			// Includes a null entry (vol == nil guard) and non-matching names.
			_, _ = w.Write([]byte(`{"Volumes":[null,{"Name":"crewship-svc-alpha-vol-data"},{"Name":"crewship-svc-beta-vol-data"},{"Name":"unrelated"}],"Warnings":null}`))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/volumes/"):
			parts := strings.Split(r.URL.Path, "/")
			removed = append(removed, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	if err := p.RemoveCrewServiceVolumes(context.Background(), "alpha"); err != nil {
		t.Fatalf("RemoveCrewServiceVolumes: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 1 || removed[0] != "crewship-svc-alpha-vol-data" {
		t.Errorf("removed = %v, want only the alpha-prefixed volume", removed)
	}
}

func TestRemoveCrewServiceVolumes_AggregatesFailures(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Volumes":[{"Name":"crewship-svc-alpha-vol-data"}],"Warnings":null}`))
		case r.Method == http.MethodDelete:
			http.Error(w, `{"message":"in use"}`, http.StatusConflict)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.RemoveCrewServiceVolumes(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), `remove 1 sidecar volume(s) for crew "alpha"`) {
		t.Fatalf("expected aggregated volume error, got %v", err)
	}
}

func TestRemoveCrewServiceVolumes_ListError(t *testing.T) {
	t.Parallel()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
	})
	err := p.RemoveCrewServiceVolumes(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "list volumes") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestRemoveCrewServiceVolumes_CustomPrefix(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var removed []string
	p := newCovProvider(t, Config{ContainerPrefix: "acme"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Volumes":[{"Name":"acme-svc-alpha-vol-pg"},{"Name":"crewship-svc-alpha-vol-pg"}],"Warnings":null}`))
		case r.Method == http.MethodDelete:
			parts := strings.Split(r.URL.Path, "/")
			removed = append(removed, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.RemoveCrewServiceVolumes(context.Background(), "alpha"); err != nil {
		t.Fatalf("RemoveCrewServiceVolumes: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 1 || removed[0] != "acme-svc-alpha-vol-pg" {
		t.Errorf("removed = %v, want only the acme-prefixed volume", removed)
	}
}

func TestComputeSidecarSpecHash_VolumeSortOrderIsCanonical(t *testing.T) {
	t.Parallel()

	// Same volumes, different author ordering — including a name tie
	// broken by mount path — must hash identically.
	a := provider.CrewService{
		Name: "pg", Image: "postgres:16",
		Volumes: []provider.CrewServiceVolume{
			{Name: "data", Mount: "/var/lib/b"},
			{Name: "data", Mount: "/var/lib/a"},
			{Name: "aux", Mount: "/aux"},
		},
	}
	b := provider.CrewService{
		Name: "pg", Image: "postgres:16",
		Volumes: []provider.CrewServiceVolume{
			{Name: "aux", Mount: "/aux"},
			{Name: "data", Mount: "/var/lib/a"},
			{Name: "data", Mount: "/var/lib/b"},
		},
	}
	if computeSidecarSpecHash(&a) != computeSidecarSpecHash(&b) {
		t.Error("volume ordering must not change the spec hash")
	}

	c := provider.CrewService{
		Name: "pg", Image: "postgres:16",
		Volumes: []provider.CrewServiceVolume{
			{Name: "data", Mount: "/var/lib/a"},
			{Name: "data", Mount: "/elsewhere"},
			{Name: "aux", Mount: "/aux"},
		},
	}
	if computeSidecarSpecHash(&a) == computeSidecarSpecHash(&c) {
		t.Error("changing a volume mount must change the spec hash")
	}
}

func TestEnsureSidecar_IgnoresUnrelatedContainers(t *testing.T) {
	t.Parallel()

	svc := covRedisSvc()
	hash := computeSidecarSpecHash(&svc)
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "other", "Names": []string{"/some-other-container"}, "Image": "nginx", "State": "running"},
				{"Id": "cid-redis", "Names": []string{"/crewship-svc-alpha-redis"}, "Image": "redis:7",
					"State": "running", "Labels": map[string]string{sidecarSpecHashLabel: hash}},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	id, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err != nil {
		t.Fatalf("ensureSidecar: %v", err)
	}
	if id != "cid-redis" {
		t.Errorf("id = %q, want cid-redis (skip non-matching names)", id)
	}
}

func TestEnsureSidecar_PullErrorPropagates(t *testing.T) {
	t.Parallel()

	svc := provider.CrewService{Name: "redis", Image: "redis:7"}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"registry down"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ensureSidecar(context.Background(), "alpha", &svc)
	if err == nil || !strings.Contains(err.Error(), "pull redis:7") {
		t.Fatalf("expected pull error from create path, got %v", err)
	}
}

func TestPullSidecarImage_DrainError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			// Truncated stream: lie about Content-Length so draining the
			// pull reader hits unexpected EOF.
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.pullSidecarImage(context.Background(), "redis:7")
	if err == nil || !strings.Contains(err.Error(), "drain pull redis:7") {
		t.Fatalf("expected drain error, got %v", err)
	}
}
