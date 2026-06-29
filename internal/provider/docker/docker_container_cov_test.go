package docker

// Coverage tests for docker_container.go: FindCrewContainer, the
// EnsureCrewRuntime branches the drift/fallback suites don't reach
// (existing-container reuse/restart, recreate triggers, error paths,
// containerEnv merge, extra mounts, privileged mode, sidecar sanity
// check, init hook opt-in), runPostStartCommands, and the remaining
// ContainerStats edge cases.
//
// All tests run against a configurable httptest fake daemon (covRT).
// Image refs point at 127.0.0.1:1 so the digest resolver's registry
// HEAD is an instant connection-refused — no real network ever.

import (
	"bytes"
	"context"
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

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

const covRuntimeRef = "127.0.0.1:1/cov/runtime:latest"

// covRT is a scriptable fake docker daemon for EnsureCrewRuntime. The
// zero value answers every call EnsureCrewRuntime makes on the happy
// create path; knobs flip individual endpoints into failure or canned
// responses. All recorded state is mutex-guarded.
type covRT struct {
	mu sync.Mutex

	// knobs
	listBody           string                                          // GET /containers/json ("" => "[]")
	listStatus         int                                             // 0 => 200
	inspectBody        string                                          // GET /containers/{id}/json ("" => 404)
	inspectStatus      int                                             // 0 => 200 (when inspectBody set)
	networksStatus     int                                             // 0 => 200 list containing cfg network
	networkName        string                                          // network reported by list
	createFn           func(req container.CreateRequest) (string, int) // nil => ("cov-cid-...", 201)
	startFn            func(id string) int                             // nil => 204
	volumeCreateFail   bool
	volumeCreateFailOn int                                         // fail only the Nth volumes/create call (1-based)
	imgInspectFn       func(call int) (string, int)                // nil => local-present body
	pullStatus         int                                         // 0 => 200 "{}"
	execCreateStatus   int                                         // 0 => 200
	execIDs            []string                                    // successive exec-create ids (last repeats)
	execStartStatus    int                                         // 0 => 200
	execInspectFn      func(execID string, call int) (string, int) // nil => not running, exit 0

	// recordings
	creates         []container.CreateRequest
	createNames     []string
	starts          []string
	stops           []string
	deletes         []string
	execCreates     []container.ExecOptions
	imgInspects     int
	execInspectN    map[string]int
	execCreateCount int
	volumeCreates   int
}

func (f *covRT) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.execInspectN == nil {
			f.execInspectN = map[string]int{}
		}
		path := r.URL.Path
		jsonHdr := func() { w.Header().Set("Content-Type", "application/json") }
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			if f.listStatus >= 400 {
				http.Error(w, `{"message":"list failed"}`, f.listStatus)
				return
			}
			jsonHdr()
			body := f.listBody
			if body == "" {
				body = "[]"
			}
			_, _ = w.Write([]byte(body))

		case strings.HasSuffix(path, "/containers/create"):
			var req container.CreateRequest
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &req)
			f.creates = append(f.creates, req)
			f.createNames = append(f.createNames, r.URL.Query().Get("name"))
			id, status := "cov-cid-0123456789ab", 201
			if f.createFn != nil {
				id, status = f.createFn(req)
			}
			if status >= 400 {
				http.Error(w, `{"message":"create failed"}`, status)
				return
			}
			jsonHdr()
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})

		// exec inspect must match before container inspect (both end /json)
		case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
			parts := strings.Split(strings.TrimSuffix(path, "/json"), "/")
			id := parts[len(parts)-1]
			f.execInspectN[id]++
			if f.execInspectFn != nil {
				body, status := f.execInspectFn(id, f.execInspectN[id])
				if status >= 400 {
					http.Error(w, body, status)
					return
				}
				jsonHdr()
				_, _ = w.Write([]byte(body))
				return
			}
			jsonHdr()
			_, _ = w.Write([]byte(`{"Running":false,"ExitCode":0}`))

		case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
			var opts container.ExecOptions
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &opts)
			f.execCreates = append(f.execCreates, opts)
			f.execCreateCount++
			if f.execCreateStatus >= 400 {
				http.Error(w, `{"message":"exec create failed"}`, f.execCreateStatus)
				return
			}
			id := "cov-exec-1"
			if len(f.execIDs) > 0 {
				id = f.execIDs[0]
				if len(f.execIDs) > 1 {
					f.execIDs = f.execIDs[1:]
				}
			}
			jsonHdr()
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})

		case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/start"):
			if f.execStartStatus >= 400 {
				http.Error(w, `{"message":"exec start failed"}`, f.execStartStatus)
				return
			}
			w.WriteHeader(http.StatusOK)

		case strings.HasSuffix(path, "/start"):
			id := strings.TrimSuffix(path, "/start")
			id = id[strings.LastIndex(id, "/")+1:]
			f.starts = append(f.starts, id)
			st := 204
			if f.startFn != nil {
				st = f.startFn(id)
			}
			if st >= 400 {
				http.Error(w, `{"message":"start failed"}`, st)
				return
			}
			w.WriteHeader(st)

		case strings.HasSuffix(path, "/stop"):
			id := strings.TrimSuffix(path, "/stop")
			id = id[strings.LastIndex(id, "/")+1:]
			f.stops = append(f.stops, id)
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(path, "/wait"):
			jsonHdr()
			_, _ = w.Write([]byte(`{"StatusCode":0}`))

		case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
			parts := strings.Split(path, "/")
			f.deletes = append(f.deletes, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)

		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			f.imgInspects++
			if f.imgInspectFn != nil {
				body, status := f.imgInspectFn(f.imgInspects)
				if status >= 400 {
					http.Error(w, body, status)
					return
				}
				jsonHdr()
				_, _ = w.Write([]byte(body))
				return
			}
			jsonHdr()
			_, _ = w.Write([]byte(`{"Id":"sha256:cov","RepoDigests":[],"Config":{"Env":["PATH=/usr/bin:/bin"]}}`))

		case strings.HasSuffix(path, "/images/create"):
			if f.pullStatus >= 400 {
				http.Error(w, `{"message":"pull failed"}`, f.pullStatus)
				return
			}
			_, _ = w.Write([]byte("{}"))

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/volumes"):
			// Volume LIST (distinct from inspect's /volumes/{name}). The legacy-C1
			// migration enumerates volumes on every EnsureCrewRuntime with a slug
			// and now fails closed if it can't; serve an empty list so it finds
			// nothing to migrate and provisioning proceeds.
			jsonHdr()
			_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": []any{}, "Warnings": nil})

		case r.Method == http.MethodGet && strings.Contains(path, "/volumes/"):
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)

		case strings.HasSuffix(path, "/volumes/create"):
			f.volumeCreates++
			if f.volumeCreateFail || (f.volumeCreateFailOn > 0 && f.volumeCreates == f.volumeCreateFailOn) {
				http.Error(w, `{"message":"volume create failed"}`, http.StatusInternalServerError)
				return
			}
			jsonHdr()
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "v"})

		case strings.HasSuffix(path, "/networks") && r.Method == http.MethodGet:
			if f.networksStatus >= 400 {
				http.Error(w, `{"message":"networks failed"}`, f.networksStatus)
				return
			}
			jsonHdr()
			name := f.networkName
			if name == "" {
				name = "covnet"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Name": name, "Id": "net-1"}})

		// container inspect — must come after the more specific /json cases
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
			if f.inspectStatus >= 400 || f.inspectBody == "" {
				st := f.inspectStatus
				if st == 0 {
					st = http.StatusNotFound
				}
				http.Error(w, `{"message":"inspect failed"}`, st)
				return
			}
			jsonHdr()
			_, _ = w.Write([]byte(f.inspectBody))

		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

// realCreate returns the captured create request for the agent container
// (User 1001:1001) — the init chown container uses 0:0 and is skipped.
func (f *covRT) realCreate(t *testing.T) *container.CreateRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.creates {
		if f.creates[i].Config != nil && f.creates[i].Config.User == "1001:1001" {
			return &f.creates[i]
		}
	}
	t.Fatal("no agent-container create captured")
	return nil
}

func covRTConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		RuntimeImage:      covRuntimeRef,
		OutputBasePath:    t.TempDir(),
		SidecarBinaryPath: "/cov/sidecar",
		EntrypointPath:    "/cov/entrypoint.sh",
	}
}

func (f *covRT) provider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.43"))
	if err != nil {
		srv.Close()
		t.Fatalf("docker client: %v", err)
	}
	p := &Provider{
		client:         cli,
		cfg:            cfg,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		digestResolver: dockerutil.NewDigestResolver(time.Hour, 2*time.Second),
	}
	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})
	return p
}

func covTeam() provider.CrewConfig {
	return provider.CrewConfig{ID: "crew1", Slug: "alpha"}
}

// inspect body for an existing crew container with all required mounts
// and the desired image.
func covHealthyInspect(image string) string {
	b, _ := json.Marshal(map[string]any{
		"Id":     "old-cid",
		"State":  map[string]any{"Running": true},
		"Config": map[string]any{"Image": image},
		"Mounts": []map[string]any{
			{"Destination": "/crew"},
			{"Destination": "/home/agent"},
			{"Destination": "/opt/crew-tools"},
		},
	})
	return string(b)
}

func covExistingList(state string) string {
	b, _ := json.Marshal([]map[string]any{{
		"Id":    "old-cid",
		"Names": []string{"/crewship-team-alpha-crew1"},
		"State": state,
	}})
	return string(b)
}

// ---------- FindCrewContainer ----------

func TestFindCrewContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		listBody    string
		listStatus  int
		wantID      string
		wantRunning bool
		wantErr     string
	}{
		{
			name:        "found running",
			listBody:    covExistingList("running"),
			wantID:      "old-cid",
			wantRunning: true,
		},
		{
			name:        "found stopped",
			listBody:    covExistingList("exited"),
			wantID:      "old-cid",
			wantRunning: false,
		},
		{
			name:     "not found",
			listBody: `[{"Id":"x","Names":["/other-team-beta"],"State":"running"}]`,
			wantID:   "",
		},
		{
			name:       "list error",
			listStatus: http.StatusInternalServerError,
			wantErr:    "list containers",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &covRT{listBody: tt.listBody, listStatus: tt.listStatus}
			p := f.provider(t, covRTConfig(t))

			id, running, err := p.FindCrewContainer(context.Background(), "crew1", "alpha")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindCrewContainer: %v", err)
			}
			if id != tt.wantID || running != tt.wantRunning {
				t.Errorf("got (%q, %v), want (%q, %v)", id, running, tt.wantID, tt.wantRunning)
			}
		})
	}
}

// ---------- EnsureCrewRuntime: validation + early errors ----------

func TestEnsureCrewRuntime_RejectsUnsafeCrewID(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "../escape", Slug: "alpha"})
	if err == nil || !strings.Contains(err.Error(), "crew id not safe for path") {
		t.Fatalf("expected unsafe-id error, got %v", err)
	}
}

func TestEnsureCrewRuntime_RejectsUnsafeSlug(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "a/b"})
	if err == nil || !strings.Contains(err.Error(), "crew slug not safe for path") {
		t.Fatalf("expected unsafe-slug error, got %v", err)
	}
}

func TestEnsureCrewRuntime_NetworkEnsureError(t *testing.T) {
	t.Parallel()
	f := &covRT{networksStatus: http.StatusInternalServerError}
	cfg := covRTConfig(t)
	cfg.Network = "covnet"
	p := f.provider(t, cfg)

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "ensure network") {
		t.Fatalf("expected ensure-network error, got %v", err)
	}
}

func TestEnsureCrewRuntime_ListError(t *testing.T) {
	t.Parallel()
	f := &covRT{listStatus: http.StatusInternalServerError}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "list containers") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestEnsureCrewRuntime_InspectExistingError(t *testing.T) {
	t.Parallel()
	f := &covRT{
		listBody:      covExistingList("running"),
		inspectStatus: http.StatusInternalServerError,
		inspectBody:   "x", // force the error path, not 404-default
	}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "inspect existing container") {
		t.Fatalf("expected inspect error, got %v", err)
	}
}

// ---------- EnsureCrewRuntime: existing-container handling ----------

func TestEnsureCrewRuntime_RecreatesWhenRequiredMountMissing(t *testing.T) {
	t.Parallel()

	inspect, _ := json.Marshal(map[string]any{
		"Id":     "old-cid",
		"State":  map[string]any{"Running": true},
		"Config": map[string]any{"Image": covRuntimeRef},
		"Mounts": []map[string]any{
			{"Destination": "/crew"},
			{"Destination": "/home/agent"},
			// /opt/crew-tools missing → must recreate
		},
	})
	f := &covRT{listBody: covExistingList("running"), inspectBody: string(inspect)}
	p := f.provider(t, covRTConfig(t))

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "cov-cid-0123456789ab" {
		t.Errorf("id = %q, want freshly created container", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	foundDelete := false
	for _, d := range f.deletes {
		if d == "old-cid" {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Errorf("old container must be removed, deletes = %v", f.deletes)
	}
}

func TestEnsureCrewRuntime_StartsExistingStoppedContainer(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
	// Pre-create the bind-mount dirs so the binds-missing check passes.
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

	f := &covRT{listBody: covExistingList("exited"), inspectBody: covHealthyInspect(covRuntimeRef)}
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "old-cid" {
		t.Errorf("id = %q, want old-cid (warm restart)", id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 0 {
		t.Errorf("warm restart must not create containers, got %d creates", len(f.creates))
	}
	if len(f.starts) != 1 || f.starts[0] != "old-cid" {
		t.Errorf("starts = %v, want [old-cid]", f.starts)
	}
}

func TestEnsureCrewRuntime_StartExistingError(t *testing.T) {
	t.Parallel()

	cfg := covRTConfig(t)
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

	f := &covRT{
		listBody:    covExistingList("exited"),
		inspectBody: covHealthyInspect(covRuntimeRef),
		startFn:     func(id string) int { return http.StatusInternalServerError },
	}
	p := f.provider(t, cfg)

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "start existing container") {
		t.Fatalf("expected start-existing error, got %v", err)
	}
}

func TestEnsureCrewRuntime_RecreatesWhenBindDirsMissing(t *testing.T) {
	t.Parallel()

	// OutputBasePath exists but the per-crew bind dirs do NOT (macOS
	// /tmp wiped on reboot scenario) → stopped container is recreated.
	f := &covRT{listBody: covExistingList("exited"), inspectBody: covHealthyInspect(covRuntimeRef)}
	p := f.provider(t, covRTConfig(t))

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "cov-cid-0123456789ab" {
		t.Errorf("id = %q, want fresh container", id)
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
		t.Errorf("stale container must be removed, deletes = %v", f.deletes)
	}
}

// ---------- EnsureCrewRuntime: create-path errors ----------

func TestEnsureCrewRuntime_EnsureImageError(t *testing.T) {
	t.Parallel()
	f := &covRT{
		imgInspectFn: func(int) (string, int) { return `{"message":"no such image"}`, http.StatusNotFound },
		pullStatus:   http.StatusInternalServerError,
	}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "ensure image") {
		t.Fatalf("expected ensure-image error, got %v", err)
	}
}

func TestEnsureCrewRuntime_MkdirError(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	cfg := covRTConfig(t)
	// OutputBasePath is a FILE → MkdirAll under it fails.
	base := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(base, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.OutputBasePath = base
	p := f.provider(t, cfg)

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "create dir") {
		t.Fatalf("expected mkdir error, got %v", err)
	}
}

func TestEnsureCrewRuntime_VolumeEnsureError(t *testing.T) {
	t.Parallel()
	f := &covRT{volumeCreateFail: true}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "volume create") {
		t.Fatalf("expected volume error, got %v", err)
	}
}

func TestEnsureCrewRuntime_ToolsVolumeEnsureError(t *testing.T) {
	t.Parallel()
	// Home volume (1st create) succeeds; tools volume (2nd) fails.
	f := &covRT{volumeCreateFailOn: 2}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "volume create crewship-tools-alpha") {
		t.Fatalf("expected tools-volume error, got %v", err)
	}
}

func TestEnsureCrewRuntime_BuildMountsErrorSurfaced(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	cfg := covRTConfig(t)
	cfg.SidecarBinaryPath = ""
	p := f.provider(t, cfg)

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "SidecarBinaryPath is required") {
		t.Fatalf("expected buildMounts error, got %v", err)
	}
}

func TestEnsureCrewRuntime_CreateError(t *testing.T) {
	t.Parallel()
	f := &covRT{
		createFn: func(req container.CreateRequest) (string, int) {
			if req.Config != nil && req.Config.User == "0:0" {
				return "init-cid", 201 // init chown container succeeds
			}
			return "", http.StatusInternalServerError
		},
	}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "container create") {
		t.Fatalf("expected create error, got %v", err)
	}
}

func TestEnsureCrewRuntime_StartError(t *testing.T) {
	t.Parallel()
	f := &covRT{
		createFn: func(req container.CreateRequest) (string, int) {
			if req.Config != nil && req.Config.User == "0:0" {
				return "init-cid", 201
			}
			return "real-cid-aaaaaaaaaaaa", 201
		},
		startFn: func(id string) int {
			if id == "real-cid-aaaaaaaaaaaa" {
				return http.StatusInternalServerError
			}
			return 204
		},
	}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err == nil || !strings.Contains(err.Error(), "container start") {
		t.Fatalf("expected start error, got %v", err)
	}
}

// ---------- EnsureCrewRuntime: init-container chown fallback ----------

func TestEnsureCrewRuntime_InitChownCreateFails_FallsBackToChmod(t *testing.T) {
	t.Parallel()
	if euid := os.Geteuid(); euid == 0 || euid == 1001 {
		// The chmod-0777 fallback only triggers when os.Chown(dir, 1001, 1001)
		// fails. Root can chown to anyone, and uid 1001 (the agent uid — and
		// the GitHub Actions "runner" user) can chown the dir to itself, so in
		// both cases chown succeeds and the fallback is unreachable.
		t.Skip("euid can chown to agent uid 1001 — chmod fallback unreachable")
	}

	f := &covRT{
		createFn: func(req container.CreateRequest) (string, int) {
			if req.Config != nil && req.Config.User == "0:0" {
				return "", http.StatusInternalServerError // init container fails
			}
			return "real-cid-aaaaaaaaaaaa", 201
		},
	}
	cfg := covRTConfig(t)
	p := f.provider(t, cfg)

	id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
	if err != nil {
		t.Fatalf("EnsureCrewRuntime must survive init-container failure: %v", err)
	}
	if id != "real-cid-aaaaaaaaaaaa" {
		t.Errorf("id = %q, want real-cid-aaaaaaaaaaaa", id)
	}
	// The fallback chmods the bind dirs to 0777.
	st, err := os.Stat(filepath.Join(cfg.OutputBasePath, "crew1"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o777 {
		t.Errorf("output dir perm = %o, want 0777 fallback", st.Mode().Perm())
	}
}

// ---------- EnsureCrewRuntime: env merge + expansion ----------

func TestEnsureCrewRuntime_ContainerEnvMergeSkipsReservedAndExpands(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	team := covTeam()
	team.ContainerEnv = map[string]string{
		"CREWSHIP_EVIL": "hijack",
		"TOOL_HOME":     "/opt/tool",
		"EXTENDED":      "${PATH}/extra",
	}
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	env := f.realCreate(t).Config.Env
	set := map[string]bool{}
	for _, e := range env {
		set[e] = true
	}
	if !set["CREWSHIP_CREW_ID=crew1"] {
		t.Errorf("platform var missing: %v", env)
	}
	if !set["TOOL_HOME=/opt/tool"] {
		t.Errorf("containerEnv var missing: %v", env)
	}
	// ${PATH} expanded against the image's default ENV (PATH=/usr/bin:/bin).
	if !set["EXTENDED=/usr/bin:/bin/extra"] {
		t.Errorf("env not expanded against image ENV: %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "CREWSHIP_EVIL=") {
			t.Errorf("reserved CREWSHIP_ key must be skipped: %v", env)
		}
	}
}

func TestEnsureCrewRuntime_ImageEnvInspectFails_ErrorWhenExpansionNeeded(t *testing.T) {
	t.Parallel()
	f := &covRT{
		imgInspectFn: func(call int) (string, int) {
			if call == 1 {
				// ensureImage's inspect: local present, skip pull.
				return `{"Id":"sha256:cov","RepoDigests":[],"Config":{"Env":[]}}`, 200
			}
			// imageEnvMap's inspect fails.
			return `{"message":"daemon hiccup"}`, http.StatusInternalServerError
		},
	}
	p := f.provider(t, covRTConfig(t))

	team := covTeam()
	team.ContainerEnv = map[string]string{"NEEDY": "${PATH}/bin"}
	_, err := p.EnsureCrewRuntime(context.Background(), team)
	if err == nil || !strings.Contains(err.Error(), "containerEnv expansion") {
		t.Fatalf("expected expansion error, got %v", err)
	}
}

func TestEnsureCrewRuntime_ImageEnvInspectFails_ProceedsWhenNoExpansionNeeded(t *testing.T) {
	t.Parallel()
	f := &covRT{
		imgInspectFn: func(call int) (string, int) {
			if call == 1 {
				return `{"Id":"sha256:cov","RepoDigests":[],"Config":{"Env":[]}}`, 200
			}
			return `{"message":"daemon hiccup"}`, http.StatusInternalServerError
		},
	}
	p := f.provider(t, covRTConfig(t))

	team := covTeam()
	team.ContainerEnv = map[string]string{"PLAIN": "literal"}
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("literal env must pass through when image inspect fails: %v", err)
	}
	env := f.realCreate(t).Config.Env
	found := false
	for _, e := range env {
		if e == "PLAIN=literal" {
			found = true
		}
	}
	if !found {
		t.Errorf("env = %v, want PLAIN=literal passed literally", env)
	}
}

// ---------- EnsureCrewRuntime: extra mounts + privileged ----------

func TestEnsureCrewRuntime_ExtraMountsAllowlist(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	team := covTeam()
	team.ExtraMounts = []provider.CrewMount{
		{Source: "/", Target: "/host"},                                   // must be rejected
		{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}, // F3: must be rejected (no longer allowlisted — host-root escape)
		{Source: "covvol", Target: "/data", Type: "volume"},              // named volume OK
		{Source: "/etc/shadow", Target: "/secrets-steal", Type: "bind"},  // must be rejected
	}
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	mounts := f.realCreate(t).HostConfig.Mounts
	byTarget := map[string]string{}
	byType := map[string]string{}
	for _, m := range mounts {
		byTarget[m.Target] = m.Source
		byType[m.Target] = string(m.Type)
	}
	if _, ok := byTarget["/host"]; ok {
		t.Error("unsafe mount source / must be rejected")
	}
	if _, ok := byTarget["/secrets-steal"]; ok {
		t.Error("unsafe mount source /etc/shadow must be rejected")
	}
	// F3 (2026-06 audit): the Docker socket is a host-root escape primitive
	// and is no longer allowlisted, so it must be dropped like any other
	// unsafe bind — even though an operator declared it in ExtraMounts.
	if _, ok := byTarget["/var/run/docker.sock"]; ok {
		t.Errorf("docker.sock mount must be rejected (F3), got: %v", byTarget)
	}
	if byTarget["/data"] != "covvol" || byType["/data"] != "volume" {
		t.Errorf("named volume mount wrong: source=%q type=%q", byTarget["/data"], byType["/data"])
	}
}

func TestEnsureCrewRuntime_PrivilegedRelaxesHardening(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	team := covTeam()
	team.Privileged = true
	team.Init = true
	team.CapAdd = []string{"NET_BIND_SERVICE"}
	team.SecurityOpt = []string{"seccomp=unconfined"}
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	hc := f.realCreate(t).HostConfig
	if !hc.Privileged {
		t.Error("Privileged must pass through")
	}
	if hc.ReadonlyRootfs {
		t.Error("privileged mode must disable read-only rootfs")
	}
	// no-new-privileges dropped; only the feature-declared opt remains.
	if len(hc.SecurityOpt) != 1 || hc.SecurityOpt[0] != "seccomp=unconfined" {
		t.Errorf("SecurityOpt = %v, want only seccomp=unconfined", hc.SecurityOpt)
	}
	foundCap := false
	for _, c := range hc.CapAdd {
		// The SDK normalizes capability names with a CAP_ prefix.
		if strings.HasSuffix(c, "NET_BIND_SERVICE") {
			foundCap = true
		}
	}
	if !foundCap {
		t.Errorf("CapAdd = %v, want NET_BIND_SERVICE", hc.CapAdd)
	}
	if hc.Init == nil || !*hc.Init {
		t.Error("Init flag must pass through as *bool true")
	}
}

func TestEnsureCrewRuntime_DefaultHardening(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	req := f.realCreate(t)
	hc := req.HostConfig
	if hc.Privileged {
		t.Error("default must not be privileged")
	}
	if !hc.ReadonlyRootfs {
		t.Error("default must use read-only rootfs")
	}
	if len(hc.SecurityOpt) != 1 || hc.SecurityOpt[0] != "no-new-privileges" {
		t.Errorf("SecurityOpt = %v, want [no-new-privileges]", hc.SecurityOpt)
	}
	if len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", hc.CapDrop)
	}
	if len(hc.CapAdd) != 0 {
		t.Errorf("CapAdd = %v, want empty (NET_RAW exfil primitive removed)", hc.CapAdd)
	}
	if hc.Init != nil {
		t.Errorf("Init = %v, want nil (docker default)", hc.Init)
	}
	if len(hc.ExtraHosts) != 1 || hc.ExtraHosts[0] != "host.docker.internal:host-gateway" {
		t.Errorf("ExtraHosts = %v", hc.ExtraHosts)
	}
	if req.Config.Entrypoint[0] != "/usr/local/bin/entrypoint.sh" {
		t.Errorf("Entrypoint = %v", req.Config.Entrypoint)
	}
	f.mu.Lock()
	name := f.createNames[len(f.createNames)-1]
	f.mu.Unlock()
	if name != "crewship-team-alpha-crew1" {
		t.Errorf("container name = %q, want crewship-team-alpha-crew1", name)
	}
}

// ---------- EnsureCrewRuntime: BYOI sidecar sanity check ----------

func covBYOITeam() provider.CrewConfig {
	team := covTeam()
	team.Image = "127.0.0.1:1/byoi/base:1"
	return team
}

func TestEnsureCrewRuntime_SanityCheck_NonZeroExitFails(t *testing.T) {
	t.Parallel()
	f := &covRT{
		execInspectFn: func(execID string, call int) (string, int) {
			return `{"Running":false,"ExitCode":3}`, 200
		},
	}
	p := f.provider(t, covRTConfig(t))

	_, err := p.EnsureCrewRuntime(context.Background(), covBYOITeam())
	if err == nil || !strings.Contains(err.Error(), "sidecar sanity check failed (exit 3)") {
		t.Fatalf("expected sanity-check error, got %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1/byoi/base:1") {
		t.Errorf("error should name the custom base image: %v", err)
	}
}

func TestEnsureCrewRuntime_SanityCheck_ZeroExitProceedsToHooks(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	team := covBYOITeam()
	team.PostStartCommands = []string{"echo ready"}
	id, err := p.EnsureCrewRuntime(context.Background(), team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "cov-cid-0123456789ab" {
		t.Errorf("id = %q", id)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// exec #1 = sanity check (root), then hooks (agent user).
	if len(f.execCreates) < 3 {
		t.Fatalf("expected sanity + breadcrumb + custom hook execs, got %d", len(f.execCreates))
	}
	sanity := f.execCreates[0]
	if sanity.User != "0:0" || strings.Join(sanity.Cmd, " ") != "/usr/local/bin/crewship-sidecar --version" {
		t.Errorf("sanity exec = user %q cmd %v", sanity.User, sanity.Cmd)
	}
	last := f.execCreates[len(f.execCreates)-1]
	if last.User != "1001:1001" {
		t.Errorf("hook exec user = %q, want 1001:1001", last.User)
	}
	if !strings.Contains(strings.Join(last.Cmd, " "), "echo ready") {
		t.Errorf("custom hook missing: %v", last.Cmd)
	}
}

func TestEnsureCrewRuntime_SanityCheck_ExecCreateFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	f := &covRT{execCreateStatus: http.StatusInternalServerError}
	p := f.provider(t, covRTConfig(t))

	id, err := p.EnsureCrewRuntime(context.Background(), covBYOITeam())
	if err != nil {
		t.Fatalf("exec-create failure must not fail container bring-up: %v", err)
	}
	if id == "" {
		t.Error("expected container id")
	}
}

func TestEnsureCrewRuntime_SanityCheck_ExecStartFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	f := &covRT{execStartStatus: http.StatusInternalServerError}
	p := f.provider(t, covRTConfig(t))

	id, err := p.EnsureCrewRuntime(context.Background(), covBYOITeam())
	if err != nil {
		t.Fatalf("exec-start failure must not fail container bring-up: %v", err)
	}
	if id == "" {
		t.Error("expected container id")
	}
}

func TestEnsureCrewRuntime_SanityCheck_InspectErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	f := &covRT{
		execInspectFn: func(execID string, call int) (string, int) {
			return `{"message":"inspect broke"}`, http.StatusInternalServerError
		},
	}
	p := f.provider(t, covRTConfig(t))

	id, err := p.EnsureCrewRuntime(context.Background(), covBYOITeam())
	if err != nil {
		t.Fatalf("exec-inspect failure must not fail container bring-up: %v", err)
	}
	if id == "" {
		t.Error("expected container id")
	}
}

func TestEnsureCrewRuntime_SanityCheck_NeverExitsGivesUpAfterPolling(t *testing.T) {
	t.Parallel()
	f := &covRT{
		execIDs: []string{"e-sanity", "e-hook"},
		execInspectFn: func(execID string, call int) (string, int) {
			if execID == "e-sanity" {
				return `{"Running":true,"ExitCode":0}`, 200 // never finishes
			}
			return `{"Running":false,"ExitCode":0}`, 200
		},
	}
	p := f.provider(t, covRTConfig(t))

	id, err := p.EnsureCrewRuntime(context.Background(), covBYOITeam())
	if err != nil {
		t.Fatalf("non-terminating sanity exec must be abandoned, not fatal: %v", err)
	}
	if id == "" {
		t.Error("expected container id")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execInspectN["e-sanity"] != 20 {
		t.Errorf("sanity poll count = %d, want 20 (bounded)", f.execInspectN["e-sanity"])
	}
}

// ---------- EnsureCrewRuntime: init hook opt-in ----------

func TestEnsureCrewRuntime_InitHookEnabled_RunsInitScript(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	team := covTeam()
	team.InitHookEnabled = true
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execCreates) == 0 {
		t.Fatal("expected at least the init-hook exec")
	}
	cmd := strings.Join(f.execCreates[0].Cmd, " ")
	if !strings.Contains(cmd, "[ -x /crew/init.sh ] && /crew/init.sh") {
		t.Errorf("init hook exec cmd = %q, want the soft-promotion script", cmd)
	}
	if env := f.execCreates[0].Env; len(env) < 2 || env[0] != "HOME=/home/agent" {
		t.Errorf("hook env = %v, want HOME=/home/agent first", env)
	}
}

func TestEnsureCrewRuntime_InitHookDisabled_OnlyBreadcrumb(t *testing.T) {
	t.Parallel()
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))

	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execCreates) == 0 {
		t.Fatal("expected the breadcrumb exec")
	}
	cmd := strings.Join(f.execCreates[0].Cmd, " ")
	if !strings.Contains(cmd, "init_hook_enabled=false") {
		t.Errorf("breadcrumb cmd = %q, want disabled-hook notice", cmd)
	}
	if strings.Contains(cmd, "&& /crew/init.sh") {
		t.Errorf("disabled hook must never execute init.sh: %q", cmd)
	}
}

// ---------- runPostStartCommands (direct) ----------

func TestRunPostStartCommands_NonZeroExitLoggedAndContinues(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	f := &covRT{
		execInspectFn: func(execID string, call int) (string, int) {
			if call == 1 {
				// still running on first poll → exercises the 50ms wait loop
				return `{"Running":true,"ExitCode":0}`, 200
			}
			return `{"Running":false,"ExitCode":7}`, 200
		},
	}
	p := f.provider(t, covRTConfig(t))
	p.logger = slog.New(slog.NewTextHandler(&covSyncWriter{buf: &logBuf, mu: &logMu}, nil))

	p.runPostStartCommands(context.Background(), "cid", []string{"exit 7", "echo after"})

	f.mu.Lock()
	execs := len(f.execCreates)
	cmd0 := strings.Join(f.execCreates[0].Cmd, " ")
	f.mu.Unlock()
	if execs != 2 {
		t.Errorf("a failing hook must not stop later hooks, got %d execs", execs)
	}
	if !strings.HasPrefix(cmd0, "bash -lc set -e") {
		t.Errorf("hooks must run via bash -lc with set -e, got %q", cmd0)
	}
	logMu.Lock()
	logs := logBuf.String()
	logMu.Unlock()
	if !strings.Contains(logs, "postStartCommand exited non-zero") {
		t.Errorf("missing non-zero warning in logs: %s", logs)
	}
	if !strings.Contains(logs, "exit_code=7") {
		t.Errorf("warning should carry exit code: %s", logs)
	}
}

func TestRunPostStartCommands_InspectErrorBreaksPolling(t *testing.T) {
	t.Parallel()

	f := &covRT{
		execInspectFn: func(execID string, call int) (string, int) {
			return `{"message":"gone"}`, http.StatusInternalServerError
		},
	}
	p := f.provider(t, covRTConfig(t))

	p.runPostStartCommands(context.Background(), "cid", []string{"true"})

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execInspectN["cov-exec-1"] != 1 {
		t.Errorf("inspect error must break the poll loop after 1 call, got %d", f.execInspectN["cov-exec-1"])
	}
}

// covSyncWriter serializes writes from slog across goroutines.
type covSyncWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w *covSyncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// ---------- ContainerStats edge cases ----------

func TestContainerStats_ClientError(t *testing.T) {
	t.Parallel()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
	})
	_, err := p.ContainerStats(context.Background(), "cid")
	if err == nil || !strings.Contains(err.Error(), "container stats") {
		t.Fatalf("expected stats error, got %v", err)
	}
}

func TestContainerStats_DecodeError(t *testing.T) {
	t.Parallel()
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	})
	_, err := p.ContainerStats(context.Background(), "cid")
	if err == nil || !strings.Contains(err.Error(), "decode stats") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestContainerStats_PercpuFallbackWhenOnlineCPUsZero(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 2_000_000_000, "percpu_usage": []int{1, 1}},
			"system_cpu_usage": 10_000_000_000,
			"online_cpus":      0,
		},
		"precpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 1_000_000_000},
			"system_cpu_usage": 5_000_000_000,
		},
		"memory_stats": map[string]any{"usage": 100, "limit": 1000, "stats": map[string]any{"cache": 0}},
	}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	})
	m, err := p.ContainerStats(context.Background(), "cid")
	if err != nil {
		t.Fatalf("ContainerStats: %v", err)
	}
	// (1e9/5e9) * len(percpu)=2 * 100 = 40
	if m.CPUPercent < 39.99 || m.CPUPercent > 40.01 {
		t.Errorf("CPUPercent = %v, want ~40 (percpu len fallback)", m.CPUPercent)
	}
}

func TestContainerStats_SingleCPUFallbackWhenNoCPUInfo(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 2_000_000_000},
			"system_cpu_usage": 10_000_000_000,
			"online_cpus":      0,
		},
		"precpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 1_000_000_000},
			"system_cpu_usage": 5_000_000_000,
		},
		"memory_stats": map[string]any{"usage": 100, "limit": 1000, "stats": map[string]any{"cache": 0}},
	}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	})
	m, err := p.ContainerStats(context.Background(), "cid")
	if err != nil {
		t.Fatalf("ContainerStats: %v", err)
	}
	// (1e9/5e9) * 1 * 100 = 20
	if m.CPUPercent < 19.99 || m.CPUPercent > 20.01 {
		t.Errorf("CPUPercent = %v, want ~20 (numCPUs=1 fallback)", m.CPUPercent)
	}
}

func TestContainerStats_CacheLargerThanUsage_FallsBackToRawUsage(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": 0}, "system_cpu_usage": 0},
		"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 0}, "system_cpu_usage": 0},
		"memory_stats": map[string]any{"usage": 100, "limit": 1000, "stats": map[string]any{"cache": 500}},
	}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	})
	m, err := p.ContainerStats(context.Background(), "cid")
	if err != nil {
		t.Fatalf("ContainerStats: %v", err)
	}
	if m.MemoryUsed != 100 {
		t.Errorf("MemoryUsed = %d, want raw usage 100 when cache > usage", m.MemoryUsed)
	}
	if m.MemoryPct < 9.99 || m.MemoryPct > 10.01 {
		t.Errorf("MemoryPct = %v, want 10", m.MemoryPct)
	}
}
