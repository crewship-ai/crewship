package docker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/client"
)

// newFakeDockerProvider returns a Provider wired to an httptest server that
// mimics a tiny slice of the Docker REST API. Lets us cover Stop / Remove /
// Status / Stats / Exec / CopyToContainer / RemoveCrewVolumes without a real
// Docker daemon. The handler is supplied by each test.
func newFakeDockerProvider(t *testing.T, handler http.HandlerFunc) (*Provider, func()) {
	t.Helper()

	srv := httptest.NewServer(handler)

	cli, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		// Pin a recent stable API version to avoid auto-negotiation calls
		// against the fake server.
		client.WithVersion("1.43"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("client: %v", err)
	}

	p := &Provider{
		client: cli,
		cfg:    Config{},
		logger: slog.Default(),
	}
	cleanup := func() {
		_ = cli.Close()
		srv.Close()
	}
	return p, cleanup
}

func TestStopCrewRuntime_FakeAPI(t *testing.T) {
	t.Parallel()

	calls := 0
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.Contains(r.URL.Path, "/containers/") || !strings.HasSuffix(r.URL.Path, "/stop") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	if err := p.StopCrewRuntime(context.Background(), "abc123"); err != nil {
		t.Fatalf("StopCrewRuntime: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 stop call, got %d", calls)
	}
}

func TestStopCrewRuntime_ErrorWraps(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
	})
	defer cleanup()

	err := p.StopCrewRuntime(context.Background(), "abcdef0123456789")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stop crew runtime") {
		t.Errorf("error should mention 'stop crew runtime': %v", err)
	}
	// Container ID should be shortID-truncated in the error message.
	if !strings.Contains(err.Error(), "abcdef012345") {
		t.Errorf("error should contain shortID: %v", err)
	}
}

func TestRemoveCrewRuntime_FakeAPI(t *testing.T) {
	t.Parallel()

	called := false
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/containers/abc123") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// force=true must be in query
		if r.URL.Query().Get("force") != "1" {
			t.Errorf("force query param should be 1, got %q", r.URL.Query().Get("force"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	if err := p.RemoveCrewRuntime(context.Background(), "abc123"); err != nil {
		t.Fatalf("RemoveCrewRuntime: %v", err)
	}
	if !called {
		t.Error("expected DELETE call")
	}
}

func TestRemoveCrewRuntime_ErrorWraps(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"already gone"}`, http.StatusInternalServerError)
	})
	defer cleanup()

	err := p.RemoveCrewRuntime(context.Background(), "deadbeefcafe9999")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "remove crew runtime") {
		t.Errorf("error should mention 'remove crew runtime': %v", err)
	}
}

// TestContainerStatus_FakeAPI_StateMapping pins the documented State string
// vocabulary against various Docker State payloads.
func TestContainerStatus_FakeAPI_StateMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     map[string]any
		wantState string
	}{
		{
			name:      "running",
			state:     map[string]any{"Running": true, "StartedAt": "2026-01-01T00:00:00Z"},
			wantState: "running",
		},
		{
			name:      "restarting => creating",
			state:     map[string]any{"Restarting": true, "StartedAt": "2026-01-01T00:00:00Z"},
			wantState: "creating",
		},
		{
			name:      "dead => error",
			state:     map[string]any{"Dead": true, "StartedAt": "2026-01-01T00:00:00Z"},
			wantState: "error",
		},
		{
			name:      "oomkilled => error",
			state:     map[string]any{"OOMKilled": true, "StartedAt": "2026-01-01T00:00:00Z"},
			wantState: "error",
		},
		{
			name:      "no flags => stopped",
			state:     map[string]any{"StartedAt": "2026-01-01T00:00:00Z"},
			wantState: "stopped",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/containers/cid/json") {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id":    "cid",
					"State": tt.state,
				})
			})
			defer cleanup()

			st, err := p.ContainerStatus(context.Background(), "cid")
			if err != nil {
				t.Fatalf("ContainerStatus: %v", err)
			}
			if st.State != tt.wantState {
				t.Errorf("state = %q, want %q", st.State, tt.wantState)
			}
			if st.ID != "cid" {
				t.Errorf("ID = %q, want cid", st.ID)
			}
			if st.Uptime != "2026-01-01T00:00:00Z" {
				t.Errorf("Uptime = %q", st.Uptime)
			}
		})
	}
}

func TestContainerStatus_InspectError(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
	})
	defer cleanup()

	_, err := p.ContainerStatus(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing container")
	}
	if !strings.Contains(err.Error(), "container inspect") {
		t.Errorf("error should mention 'container inspect': %v", err)
	}
}

// TestContainerStats_FakeAPI exercises the metrics arithmetic with a
// hand-rolled stats payload. Verifies CPU/memory delta math without a real
// daemon.
func TestContainerStats_FakeAPI(t *testing.T) {
	t.Parallel()

	statsPayload := map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 2_000_000_000},
			"system_cpu_usage": 10_000_000_000,
			"online_cpus":      2,
		},
		"precpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 1_000_000_000},
			"system_cpu_usage": 5_000_000_000,
		},
		"memory_stats": map[string]any{
			"usage": 200,
			"limit": 1000,
			"stats": map[string]any{"cache": 50},
		},
		"networks": map[string]any{
			"eth0": map[string]any{"rx_bytes": 10, "tx_bytes": 20},
		},
		"pids_stats": map[string]any{"current": 7},
	}

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/containers/cid/stats") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// SDK passes stream=0 for one-shot.
		if r.URL.Query().Get("stream") != "0" {
			t.Errorf("stream query = %q, want 0", r.URL.Query().Get("stream"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statsPayload)
	})
	defer cleanup()

	m, err := p.ContainerStats(context.Background(), "cid")
	if err != nil {
		t.Fatalf("ContainerStats: %v", err)
	}

	// cpuDelta=1e9, sysDelta=5e9, numCPUs=2 => (1/5)*2*100 = 40.0
	if m.CPUPercent < 39.99 || m.CPUPercent > 40.01 {
		t.Errorf("CPUPercent = %v, want ~40", m.CPUPercent)
	}
	// memUsed = 200 - 50 = 150; memLimit = 1000; memPct = 15
	if m.MemoryUsed != 150 {
		t.Errorf("MemoryUsed = %d, want 150", m.MemoryUsed)
	}
	if m.MemoryLimit != 1000 {
		t.Errorf("MemoryLimit = %d, want 1000", m.MemoryLimit)
	}
	if m.MemoryPct < 14.99 || m.MemoryPct > 15.01 {
		t.Errorf("MemoryPct = %v, want 15", m.MemoryPct)
	}
	if m.NetRx != 10 || m.NetTx != 20 {
		t.Errorf("network bytes wrong: rx=%d tx=%d", m.NetRx, m.NetTx)
	}
	if m.PIDs != 7 {
		t.Errorf("PIDs = %d, want 7", m.PIDs)
	}
	if m.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestContainerStats_HandlesCPUWraparound(t *testing.T) {
	t.Parallel()

	// pre > current => guard kicks in, cpuPct stays 0.
	statsPayload := map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 1_000_000_000},
			"system_cpu_usage": 5_000_000_000,
			"online_cpus":      1,
		},
		"precpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": 2_000_000_000},
			"system_cpu_usage": 10_000_000_000,
		},
		"memory_stats": map[string]any{"usage": 100, "limit": 0, "stats": map[string]any{"cache": 0}},
		"networks":     map[string]any{},
		"pids_stats":   map[string]any{"current": 0},
	}

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(statsPayload)
	})
	defer cleanup()

	m, err := p.ContainerStats(context.Background(), "cid")
	if err != nil {
		t.Fatalf("ContainerStats: %v", err)
	}
	if m.CPUPercent != 0 {
		t.Errorf("CPUPercent should be 0 on wraparound, got %v", m.CPUPercent)
	}
	if m.MemoryPct != 0 {
		t.Errorf("MemoryPct should be 0 with zero limit, got %v", m.MemoryPct)
	}
}

func TestExecInspect_FakeAPI(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/exec/exec-99/json") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Running":  false,
			"ExitCode": 42,
		})
	})
	defer cleanup()

	running, code, err := p.ExecInspect(context.Background(), "exec-99")
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if running {
		t.Error("expected Running=false")
	}
	if code != 42 {
		t.Errorf("ExitCode = %d, want 42", code)
	}
}

func TestExecInspect_ErrorWraps(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no such exec"}`, http.StatusNotFound)
	})
	defer cleanup()

	_, _, err := p.ExecInspect(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "exec inspect") {
		t.Errorf("error should mention 'exec inspect': %v", err)
	}
}

func TestRemoveCrewVolumes_FakeAPI(t *testing.T) {
	t.Parallel()

	deletes := []string{}
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/volumes/") {
			parts := strings.Split(r.URL.Path, "/")
			deletes = append(deletes, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()

	p.cfg.ContainerPrefix = "crewship"
	if err := p.RemoveCrewVolumes(context.Background(), "alpha"); err != nil {
		t.Fatalf("RemoveCrewVolumes: %v", err)
	}
	if len(deletes) != 2 {
		t.Fatalf("expected 2 volume deletes, got %d (%v)", len(deletes), deletes)
	}
	want := map[string]bool{"crewship-home-alpha": true, "crewship-tools-alpha": true}
	for _, d := range deletes {
		if !want[d] {
			t.Errorf("unexpected volume delete %q", d)
		}
	}
}

func TestRemoveCrewVolumes_FailureIsNonFatal(t *testing.T) {
	t.Parallel()

	// Volume removal failures are logged-and-continued, not fatal — agents
	// can still operate with stale volumes.
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"volume in use"}`, http.StatusConflict)
	})
	defer cleanup()

	if err := p.RemoveCrewVolumes(context.Background(), "alpha"); err != nil {
		t.Errorf("RemoveCrewVolumes should not propagate per-volume errors: %v", err)
	}
}

func TestEnsureNetwork_AlreadyExists_NoCreate(t *testing.T) {
	t.Parallel()

	createCalls := 0
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks"):
			// Returns a network with the requested name already present.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Name": "crewship-agents", "Id": "net-1"},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/networks/create"):
			createCalls++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "net-x"})
		default:
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer cleanup()

	if err := p.ensureNetwork(context.Background(), "crewship-agents"); err != nil {
		t.Fatalf("ensureNetwork: %v", err)
	}
	if createCalls != 0 {
		t.Errorf("create should not be called when network exists, got %d", createCalls)
	}
}

func TestEnsureNetwork_CreatesWhenAbsent(t *testing.T) {
	t.Parallel()

	createCalls := 0
	var createBody map[string]any
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/networks/create"):
			createCalls++
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &createBody)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "net-new"})
		default:
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer cleanup()

	if err := p.ensureNetwork(context.Background(), "fresh-net"); err != nil {
		t.Fatalf("ensureNetwork: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("expected 1 create call, got %d", createCalls)
	}
	if name, _ := createBody["Name"].(string); name != "fresh-net" {
		t.Errorf("create body Name = %q, want fresh-net", name)
	}
	if drv, _ := createBody["Driver"].(string); drv != "bridge" {
		t.Errorf("create Driver = %q, want bridge", drv)
	}
	// CRITICAL: must NOT be Internal=true. Internal networks block the
	// sidecar from reaching api.anthropic.com (CLAUDE.md anti-pattern).
	if internal, ok := createBody["Internal"].(bool); ok && internal {
		t.Error("network must NOT be created with Internal=true (would block agent internet)")
	}
}

func TestEnsureNetwork_ListError(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"daemon down"}`, http.StatusInternalServerError)
	})
	defer cleanup()

	err := p.ensureNetwork(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "list networks") {
		t.Errorf("error should mention 'list networks': %v", err)
	}
}

func TestExec_CreateError(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
	})
	defer cleanup()

	_, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "missing",
		Cmd:         []string{"echo"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "exec create") {
		t.Errorf("error should mention 'exec create': %v", err)
	}
}

func TestExecResize_FakeAPI(t *testing.T) {
	t.Parallel()

	called := false
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if !strings.Contains(r.URL.Path, "/exec/exec-1/resize") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("h") != "30" || r.URL.Query().Get("w") != "120" {
			t.Errorf("resize dims wrong: h=%s w=%s",
				r.URL.Query().Get("h"), r.URL.Query().Get("w"))
		}
		w.WriteHeader(http.StatusOK)
	})
	defer cleanup()

	if err := p.ExecResize(context.Background(), "exec-1", 30, 120); err != nil {
		t.Fatalf("ExecResize: %v", err)
	}
	if !called {
		t.Error("expected resize call")
	}
}

func TestCopyToContainer_FakeAPI(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenQuery string
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		seenPath = r.URL.Path
		seenQuery = r.URL.Query().Get("path")
		w.WriteHeader(http.StatusOK)
	})
	defer cleanup()

	body := strings.NewReader("tar bytes here")
	if err := p.CopyToContainer(context.Background(), "cid", "/workspace/in", body); err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}
	if !strings.Contains(seenPath, "/containers/cid/archive") {
		t.Errorf("path = %q", seenPath)
	}
	if seenQuery != "/workspace/in" {
		t.Errorf("destination query = %q, want /workspace/in", seenQuery)
	}
}

func TestProviderClose_NoPanicOnNoopClient(t *testing.T) {
	t.Parallel()
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestProvider_ContextDeadlineRespected(t *testing.T) {
	t.Parallel()

	// Slow handler — sleeps longer than ctx deadline.
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := p.StopCrewRuntime(ctx, "abc")
	if err == nil {
		t.Fatal("expected context deadline error")
	}
}
