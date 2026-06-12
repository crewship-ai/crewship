package docker

// Coverage tests for docker.go: Detect (DOCKER_HOST branch), New,
// ensureImage (digest reconciliation + pull fallbacks), ensureNetwork
// create-error, ensureVolume (mountpoint resurrection), Exec /
// ExecInteractive (hijacked streams) and ContainerIP.
//
// No real daemon, no real registry, no real network: every endpoint —
// including the OCI registry the digest resolver HEADs — is an httptest
// server, and deliberately-unreachable refs use 127.0.0.1:1 (instant
// connection-refused).

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
)

// covDaemonHandler fakes the minimal daemon surface Detect/New touch:
// /_ping (with configurable headers/status) and /version.
func covDaemonHandler(t *testing.T, pingStatus int, pingHeaders map[string]string, versionBody map[string]any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			for k, v := range pingHeaders {
				w.Header().Set(k, v)
			}
			w.WriteHeader(pingStatus)
		case strings.HasSuffix(r.URL.Path, "/version"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(versionBody)
		case strings.HasSuffix(r.URL.Path, "/networks") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "net-1"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

// NOTE: Detect/New tests mutate DOCKER_HOST via t.Setenv, so they must
// NOT be parallel.

func TestDetect_DockerHost_Docker(t *testing.T) {
	srv := httptest.NewServer(covDaemonHandler(t, http.StatusOK,
		map[string]string{"Api-Version": "1.43"},
		map[string]any{"Version": "24.0.7", "ApiVersion": "1.43"},
	))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)

	res, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Runtime != "docker" {
		t.Errorf("Runtime = %q, want docker", res.Runtime)
	}
	if res.Socket != srv.URL {
		t.Errorf("Socket = %q, want %q", res.Socket, srv.URL)
	}
	if res.Version != "24.0.7" {
		t.Errorf("Version = %q, want 24.0.7", res.Version)
	}
}

func TestDetect_DockerHost_PodmanByComponents(t *testing.T) {
	srv := httptest.NewServer(covDaemonHandler(t, http.StatusOK,
		map[string]string{"Api-Version": "1.41"},
		map[string]any{
			"Version": "4.9.3-docker-compat",
			"Components": []map[string]any{
				{"Name": "Podman Engine", "Version": "4.9.3"},
			},
		},
	))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)

	res, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Runtime != "podman" {
		t.Errorf("Runtime = %q, want podman", res.Runtime)
	}
	if res.Version != "4.9.3" {
		t.Errorf("Version = %q, want component version 4.9.3", res.Version)
	}
}

func TestDetect_DockerHost_PodmanByLibpodAPIVersion(t *testing.T) {
	srv := httptest.NewServer(covDaemonHandler(t, http.StatusOK,
		map[string]string{"Api-Version": "4.0.0-libpod"},
		map[string]any{"Version": "4.0.0"},
	))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)

	res, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Runtime != "podman" {
		t.Errorf("Runtime = %q, want podman (libpod API version)", res.Runtime)
	}
}

func TestDetect_DockerHost_PingError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"daemon sad"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)

	_, err := Detect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker ping") {
		t.Fatalf("expected ping error, got %v", err)
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error should name the DOCKER_HOST: %v", err)
	}
}

func TestDetect_DockerHost_InvalidURL(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://[invalid")

	_, err := Detect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker client") {
		t.Fatalf("expected client construction error, got %v", err)
	}
}

func TestNew_DockerHost_HappyPathEnsuresNetwork(t *testing.T) {
	var mu sync.Mutex
	var netCreateBody map[string]any
	base := covDaemonHandler(t, http.StatusOK,
		map[string]string{"Api-Version": "1.43"},
		map[string]any{"Version": "24.0.7"},
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/networks/create") {
			mu.Lock()
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &netCreateBody)
			mu.Unlock()
		}
		base(w, r)
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)

	// nil logger exercises the slog.Default() fallback.
	p, err := New(context.Background(), Config{Network: "covnet"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	if p.Detected().Runtime != "docker" {
		t.Errorf("Detected().Runtime = %q", p.Detected().Runtime)
	}
	if p.DockerClient() == nil {
		t.Error("DockerClient() should not be nil")
	}
	mu.Lock()
	defer mu.Unlock()
	if name, _ := netCreateBody["Name"].(string); name != "covnet" {
		t.Errorf("network create Name = %q, want covnet", name)
	}
}

func TestNew_DetectFailureWrapped(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://[invalid")

	_, err := New(context.Background(), Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "container runtime:") {
		t.Fatalf("expected wrapped detect error, got %v", err)
	}
}

func TestNew_NetworkEnsureFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.43")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Version": "24.0.7"})
		default:
			// Network list/create fail — New must still succeed (warn only).
			http.Error(w, `{"message":"nope"}`, http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)

	p, err := New(context.Background(), Config{Network: "covnet"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New should tolerate ensureNetwork failure: %v", err)
	}
	_ = p.Close()
}

func TestEnsureNetwork_CreateError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			http.Error(w, `{"message":"address pool exhausted"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.ensureNetwork(context.Background(), "covnet")
	if err == nil || !strings.Contains(err.Error(), "create network") {
		t.Fatalf("expected create-network error, got %v", err)
	}
}

// ---------- ensureImage ----------

const covDigest = "sha256:abababababababababababababababababababababababababababababababab"

// newCovImageProvider builds a Provider whose single httptest server
// doubles as the docker daemon (/v1.43/...) AND the OCI registry (/v2/...)
// for the returned image ref. registryDigest == "" makes manifest HEADs
// 404 (remote digest unknown).
func newCovImageProvider(t *testing.T, registryDigest string, docker http.HandlerFunc) (*Provider, string) {
	t.Helper()

	var srvURL string
	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/") || r.URL.Path == "/v2" {
			switch {
			case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
				w.WriteHeader(http.StatusOK)
			case strings.Contains(r.URL.Path, "/manifests/") && registryDigest != "":
				w.Header().Set("Docker-Content-Digest", registryDigest)
				w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
				w.Header().Set("Content-Length", "2")
				w.WriteHeader(http.StatusOK)
			default:
				http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
			}
			return
		}
		docker(w, r)
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))
	srvURL = srv.URL
	t.Cleanup(srv.Close)

	p := newCovProvider(t, Config{}, handler)
	// p has its own server; point the image ref at THIS server's /v2 so
	// the registry HEAD lands on our fake. The docker API calls go through
	// p's client (separate server, same handler) — both observe the same
	// closures.
	host := strings.TrimPrefix(srvURL, "http://")
	return p, host + "/cov/img:tag"
}

func TestEnsureImage_LocalMatchesRemoteDigest_NoPull(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	pulls := 0
	var ref string
	p, ref := newCovImageProvider(t, covDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{strings.Split(ref, ":tag")[0] + "@" + covDigest},
			})
		case strings.HasSuffix(path, "/images/create"):
			mu.Lock()
			pulls++
			mu.Unlock()
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if pulls != 0 {
		t.Errorf("digest match must not pull, got %d pulls", pulls)
	}
}

func TestEnsureImage_LocalPresentRemoteUnknown_NoPull(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	pulls := 0
	p, ref := newCovImageProvider(t, "" /* registry 404s */, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:local", "RepoDigests": []string{}})
		case strings.HasSuffix(path, "/images/create"):
			mu.Lock()
			pulls++
			mu.Unlock()
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if pulls != 0 {
		t.Errorf("offline + local copy must not pull, got %d pulls", pulls)
	}
}

func TestEnsureImage_StaleLocal_Repulls(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	pulls := 0
	p, ref := newCovImageProvider(t, covDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{"cov/img@sha256:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"},
			})
		case strings.HasSuffix(path, "/images/create"):
			mu.Lock()
			pulls++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"pulling"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if pulls != 1 {
		t.Errorf("stale local must trigger exactly 1 pull, got %d", pulls)
	}
}

func TestEnsureImage_PullFailsButLocalPresent_Proceeds(t *testing.T) {
	t.Parallel()

	p, ref := newCovImageProvider(t, covDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{"cov/img@sha256:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"},
			})
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"registry flaked"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("pull failure with a local copy must be tolerated: %v", err)
	}
}

func TestEnsureImage_PullFailsNoLocal_Errors(t *testing.T) {
	t.Parallel()

	p, ref := newCovImageProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"registry down"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.ensureImage(context.Background(), ref)
	if err == nil || !strings.Contains(err.Error(), "pull image") {
		t.Fatalf("expected pull error, got %v", err)
	}
}

func TestEnsureImage_FreshPullSucceeds(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	pulls := 0
	p, ref := newCovImageProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			mu.Lock()
			pulls++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":"downloading"}` + "\n"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if pulls != 1 {
		t.Errorf("expected exactly 1 pull, got %d", pulls)
	}
}

func TestEnsureImage_DrainErrorSurfaces(t *testing.T) {
	t.Parallel()

	p, ref := newCovImageProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			// Lie about Content-Length so the client's io.Copy hits an
			// unexpected EOF mid-stream.
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.ensureImage(context.Background(), ref)
	if err == nil || !strings.Contains(err.Error(), "drain pull stream") {
		t.Fatalf("expected drain error, got %v", err)
	}
}

// ---------- ensureVolume ----------

func TestEnsureVolume_HealthyExisting_NoCreate(t *testing.T) {
	t.Parallel()

	mountpoint := t.TempDir() // exists on disk
	var mu sync.Mutex
	creates := 0
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name": "v1", "Driver": "local", "Mountpoint": mountpoint, "Scope": "local",
			})
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			creates++
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v1"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureVolume(context.Background(), "v1"); err != nil {
		t.Fatalf("ensureVolume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 0 {
		t.Errorf("healthy volume must not be recreated, got %d creates", creates)
	}
}

func TestEnsureVolume_MountpointVanished_RemoveAndRecreate(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), "vanished", "_data")
	var mu sync.Mutex
	var removed, created bool
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name": "v1", "Driver": "local", "Mountpoint": gone, "Scope": "local",
			})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/volumes/"):
			removed = true
			if r.URL.Query().Get("force") != "1" {
				t.Errorf("vanished-mountpoint removal should be forced")
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			created = true
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v1"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureVolume(context.Background(), "v1"); err != nil {
		t.Fatalf("ensureVolume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !removed || !created {
		t.Errorf("removed=%v created=%v, want both true", removed, created)
	}
}

func TestEnsureVolume_MountpointVanished_RemoveFails(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), "vanished")
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name": "v1", "Driver": "local", "Mountpoint": gone, "Scope": "local",
			})
		case r.Method == http.MethodDelete:
			http.Error(w, `{"message":"volume in use"}`, http.StatusConflict)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.ensureVolume(context.Background(), "v1")
	if err == nil || !strings.Contains(err.Error(), "volume remove (mountpoint vanished)") {
		t.Fatalf("expected remove error, got %v", err)
	}
}

func TestEnsureVolume_StatPermissionDenied_TreatedHealthy(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root — EACCES not reproducible")
	}

	// 0o000 parent dir → os.Stat on a child returns EACCES, not ENOENT.
	parent := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	inaccessible := filepath.Join(parent, "_data")

	var mu sync.Mutex
	creates := 0
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Name": "v1", "Driver": "local", "Mountpoint": inaccessible, "Scope": "local",
			})
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			creates++
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v1"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureVolume(context.Background(), "v1"); err != nil {
		t.Fatalf("EACCES must be treated as healthy: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 0 {
		t.Errorf("EACCES volume must not be recreated, got %d creates", creates)
	}
}

func TestEnsureVolume_EmptyMountpoint_FallsThroughToCreate(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	creates := 0
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v1", "Mountpoint": ""})
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			creates++
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v1"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if err := p.ensureVolume(context.Background(), "v1"); err != nil {
		t.Fatalf("ensureVolume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 1 {
		t.Errorf("expected idempotent create, got %d", creates)
	}
}

func TestEnsureVolume_CreateError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/volumes/"):
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			http.Error(w, `{"message":"disk full"}`, http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	err := p.ensureVolume(context.Background(), "v1")
	if err == nil || !strings.Contains(err.Error(), "volume create v1") {
		t.Fatalf("expected create error, got %v", err)
	}
}

// ---------- Exec / ExecInteractive (hijacked connections) ----------

// covStdcopyFrame builds one docker stdcopy multiplexing frame
// (stream byte, 3 zero bytes, 4-byte big-endian length, payload).
func covStdcopyFrame(stream byte, payload string) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:8], uint32(len(payload)))
	copy(b[8:], payload)
	return b
}

// covHijackUpgrade hijacks the HTTP connection and replies with the 101
// raw-stream upgrade docker uses for exec attach, then writes raw and
// closes.
func covHijackUpgrade(t *testing.T, w http.ResponseWriter, r *http.Request, raw []byte) {
	t.Helper()
	// Drain the request body BEFORE hijacking — leftover unread bytes
	// would make the eventual Close send a TCP RST, racing the client's
	// read of our payload.
	_, _ = io.Copy(io.Discard, r.Body)
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("response writer does not support hijacking")
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	defer conn.Close()
	_, _ = bufrw.WriteString("HTTP/1.1 101 UPGRADED\r\n" +
		"Content-Type: application/vnd.docker.raw-stream\r\n" +
		"Connection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	_, _ = bufrw.Write(raw)
	_ = bufrw.Flush()
	// Half-close so the client sees a clean EOF, then wait (bounded)
	// for the peer to finish reading before the deferred full Close.
	if tc, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = tc.CloseWrite()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
	}
}

func TestExec_Success_DemuxesStdoutAndStderr(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var execOpts container.ExecOptions
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/cid/exec"):
			mu.Lock()
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &execOpts)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "e1"})
		case strings.Contains(path, "/exec/e1/start"):
			frames := append(covStdcopyFrame(1, "hello "), covStdcopyFrame(2, "errs")...)
			covHijackUpgrade(t, w, r, frames)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid",
		Cmd:         []string{"echo", "hi"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExecID != "e1" {
		t.Errorf("ExecID = %q, want e1", res.ExecID)
	}
	out, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "hello errs" {
		t.Errorf("combined output = %q, want %q", out, "hello errs")
	}

	mu.Lock()
	defer mu.Unlock()
	if execOpts.User != "1001:1001" {
		t.Errorf("default exec user = %q, want 1001:1001", execOpts.User)
	}
	if !execOpts.AttachStdout || !execOpts.AttachStderr {
		t.Error("exec must attach stdout+stderr")
	}
	if execOpts.Tty {
		t.Error("non-interactive exec must not request a TTY")
	}
}

func TestExec_AttachError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "e1"})
		default:
			http.Error(w, `{"message":"cannot attach"}`, http.StatusInternalServerError)
		}
	})

	_, err := p.Exec(context.Background(), provider.ExecConfig{ContainerID: "cid", Cmd: []string{"ls"}})
	if err == nil || !strings.Contains(err.Error(), "exec attach") {
		t.Fatalf("expected attach error, got %v", err)
	}
}

func TestExecInteractive_Success(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var execOpts container.ExecOptions
	var resizeQuery string
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/cid/exec"):
			mu.Lock()
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &execOpts)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "e2"})
		case strings.Contains(path, "/exec/e2/resize"):
			mu.Lock()
			resizeQuery = r.URL.RawQuery
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case strings.Contains(path, "/exec/e2/start"):
			covHijackUpgrade(t, w, r, []byte("tty-bytes"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	res, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid",
		Cmd:         []string{"bash"},
		User:        "0:0",
		Rows:        24,
		Cols:        80,
	})
	if err != nil {
		t.Fatalf("ExecInteractive: %v", err)
	}
	defer res.Conn.Close()

	if res.ExecID != "e2" {
		t.Errorf("ExecID = %q, want e2", res.ExecID)
	}
	out, err := io.ReadAll(res.Conn)
	if err != nil {
		t.Fatalf("read conn: %v", err)
	}
	if string(out) != "tty-bytes" {
		t.Errorf("conn data = %q, want tty-bytes", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if !execOpts.Tty || !execOpts.AttachStdin {
		t.Errorf("interactive exec must request Tty + stdin: %+v", execOpts)
	}
	if execOpts.User != "0:0" {
		t.Errorf("explicit user must pass through, got %q", execOpts.User)
	}
	if !strings.Contains(resizeQuery, "h=24") || !strings.Contains(resizeQuery, "w=80") {
		t.Errorf("initial resize query = %q, want h=24 w=80", resizeQuery)
	}
}

func TestExecInteractive_CreateError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
	})

	_, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "missing", Cmd: []string{"bash"},
	})
	if err == nil || !strings.Contains(err.Error(), "exec interactive create") {
		t.Fatalf("expected create error, got %v", err)
	}
}

func TestExecInteractive_AttachError(t *testing.T) {
	t.Parallel()

	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exec") && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "e3"})
			return
		}
		http.Error(w, `{"message":"attach denied"}`, http.StatusInternalServerError)
	})

	_, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid", Cmd: []string{"bash"},
	})
	if err == nil || !strings.Contains(err.Error(), "exec interactive attach") {
		t.Fatalf("expected attach error, got %v", err)
	}
}

// ---------- ContainerIP ----------

func TestContainerIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		status  int
		wantIP  string
		wantErr string
	}{
		{
			name:   "attached with IP",
			body:   `{"Id":"cid","NetworkSettings":{"Networks":{"covnet":{"IPAddress":"172.18.0.5"}}}}`,
			status: http.StatusOK,
			wantIP: "172.18.0.5",
		},
		{
			name:    "not attached to requested network",
			body:    `{"Id":"cid","NetworkSettings":{"Networks":{"other":{"IPAddress":"10.0.0.9"}}}}`,
			status:  http.StatusOK,
			wantErr: `not attached to network "covnet"`,
		},
		{
			name:    "attached but empty IP",
			body:    `{"Id":"cid","NetworkSettings":{"Networks":{"covnet":{"IPAddress":""}}}}`,
			status:  http.StatusOK,
			wantErr: `not attached to network "covnet"`,
		},
		{
			name:    "no network settings",
			body:    `{"Id":"cid"}`,
			status:  http.StatusOK,
			wantErr: "no network settings",
		},
		{
			name:    "inspect error",
			body:    `{"message":"no such container"}`,
			status:  http.StatusNotFound,
			wantErr: "inspect container",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			ip, err := p.ContainerIP(context.Background(), "cid", "covnet")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ContainerIP: %v", err)
			}
			if ip != tt.wantIP {
				t.Errorf("ip = %q, want %q", ip, tt.wantIP)
			}
		})
	}
}
