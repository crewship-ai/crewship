package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestDaemonStorageMapping(t *testing.T) {
	root := mappingTempDir(t)
	if err := os.Mkdir(filepath.Join(root, "with spaces"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("with spaces", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mappingTempDir(t), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	p := &Provider{pathMap: &daemonPathMap{local: root, daemon: "/daemon/volume"}}
	for _, tc := range []struct {
		path, want string
		bad        bool
	}{
		{root + "/with spaces/output", "/daemon/volume/with spaces/output", false},
		{root + "/alias/output", "/daemon/volume/with spaces/output", false},
		{root + "-other/workspace", "", true}, {root + "/../outside", "", true},
		{root + "/escape/output", "", true}, {"relative", "", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := p.pathMap.translate(tc.path)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("translate(%s)=%q,%v", tc.path, got, err)
			}
		})
	}
	input := []mount.Mount{{Type: mount.TypeBind, Source: root + "/sidecar", Target: "/helper", ReadOnly: true}, noexecBindMount(root+"/with spaces/output", "/output", "crew", "installation"), {Type: mount.TypeVolume, Source: "home", Target: "/home/agent"}}
	got, err := p.daemonMounts(input)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Source != "/daemon/volume/sidecar" || !got[0].ReadOnly || got[1].VolumeOptions.DriverConfig.Options["device"] != "/daemon/volume/with spaces/output" || got[1].VolumeOptions.DriverConfig.Options["o"] != "bind,noexec,nosuid" || !got[1].VolumeOptions.NoCopy || got[2].Source != "home" {
		t.Fatalf("wrong translated mounts: %+v", got)
	}
	if input[0].Source != root+"/sidecar" || input[1].VolumeOptions.DriverConfig.Options["device"] != root+"/with spaces/output" {
		t.Fatal("translation changed local input")
	}
}

func TestDaemonPathsResolveInspect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mounts []map[string]any
		bad    bool
	}{
		{"named volume", 200, []map[string]any{{"Type": "volume", "Driver": "local", "Source": "/docker/volume/_data", "Destination": "/data", "RW": true}}, false},
		{"bind", 200, []map[string]any{{"Type": "bind", "Source": "/host/data", "Destination": "/data", "RW": true}}, false},
		{"readonly parent first", 200, []map[string]any{{"Type": "bind", "Source": "/host/readonly", "Destination": "/data", "RW": false}, {"Type": "volume", "Driver": "local", "Source": "/docker/volume/storage", "Destination": "/data/storage", "RW": true}}, false},
		{"readonly parent last", 200, []map[string]any{{"Type": "volume", "Driver": "local", "Source": "/docker/volume/storage", "Destination": "/data/storage", "RW": true}, {"Type": "bind", "Source": "/host/readonly", "Destination": "/data", "RW": false}}, false},
		{"tmpfs ancestor", 200, []map[string]any{{"Type": "tmpfs", "Destination": "/data", "RW": true}, {"Type": "volume", "Driver": "local", "Source": "/docker/volume/storage", "Destination": "/data/storage", "RW": true}}, false},
		{"foreign hostname", 200, []map[string]any{{"Type": "volume", "Driver": "local", "Source": "/docker/data", "Destination": "/data", "RW": true}}, true},
		{"foreign volume driver", 200, []map[string]any{{"Type": "volume", "Driver": "remote", "Source": "/docker/data", "Destination": "/data", "RW": true}}, true},
		{"unknown volume driver", 200, []map[string]any{{"Type": "volume", "Source": "/docker/data", "Destination": "/data", "RW": true}}, true},
		{"missing mapping", 200, nil, true}, {"proxy denied", 403, nil, true}, {"daemon error", 500, nil, true},
		{"readonly", 200, []map[string]any{{"Type": "volume", "Driver": "local", "Source": "/docker/data", "Destination": "/data", "RW": false}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/containers/server/json") {
					w.WriteHeader(tc.status)
					hostname, _ := os.Hostname()
					if tc.name == "foreign hostname" {
						hostname = "another-server"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Mounts": tc.mounts, "Config": map[string]any{"Hostname": hostname}})
					return
				}
				w.Header().Set("API-Version", "1.47")
				fmt.Fprint(w, "OK")
			}))
			defer server.Close()
			cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			p := &Provider{client: cli, cfg: Config{OutputBasePath: "/data/storage", ServerContainer: "server"}}
			m, err := p.resolveDaemonPaths(context.Background())
			if (err != nil) != tc.bad {
				t.Fatalf("map=%+v err=%v", m, err)
			}
			if !tc.bad && (!strings.HasSuffix(m.daemon, "/storage") || m.local != "/data/storage") {
				t.Fatalf("bad map: %+v", m)
			}
		})
	}
}

func TestMappedRuntimeContractStable(t *testing.T) {
	cfg := covRTConfig(t)
	cfg.OutputBasePath = mappingTempDir(t)
	cfg.SidecarBinaryPath = filepath.Join(cfg.OutputBasePath, "sidecar")
	cfg.EntrypointPath = filepath.Join(cfg.OutputBasePath, "entrypoint.sh")
	if err := os.WriteFile(cfg.SidecarBinaryPath, []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.EntrypointPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// Stage real local fixtures, then build using daemon-only sources.
	cfg = stageRuntimeArtifacts(cfg, quietLogger())
	makeProvider := func() *Provider {
		return &Provider{cfg: cfg, logger: quietLogger(), pathMap: &daemonPathMap{local: cfg.OutputBasePath, daemon: "/daemon/data"}}
	}
	a, b := makeProvider().crewRuntimeContractDigest(), makeProvider().crewRuntimeContractDigest()
	if a == "" || a != b {
		t.Fatalf("unstable mapped runtime contract: %q %q", a, b)
	}
}

func TestBuildMountsUsesDaemonPaths(t *testing.T) {
	root := mappingTempDir(t)
	for _, name := range []string{"sidecar", "entrypoint"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
			t.Fatal(err)
		}
	}
	p := &Provider{cfg: Config{SidecarBinaryPath: root + "/sidecar", EntrypointPath: root + "/entrypoint"}, pathMap: &daemonPathMap{local: root, daemon: "/daemon/data"}}
	got, err := p.buildMounts("crew", "slug", root+"/workspace", root+"/output", root+"/crew")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.Type == mount.TypeBind && !strings.HasPrefix(m.Source, "/daemon/data/") {
			t.Fatalf("server-local runtime bind leaked to daemon: %s", m.Source)
		}
		if m.VolumeOptions != nil && m.VolumeOptions.DriverConfig != nil && !strings.HasPrefix(m.VolumeOptions.DriverConfig.Options["device"], "/daemon/data/") {
			t.Fatalf("server-local volume device leaked to daemon: %+v", m)
		}
	}
	if p.cfg.SidecarBinaryPath != root+"/sidecar" {
		t.Fatal("local path overwritten")
	}
}

func TestMandatoryBindPreflightClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		message string
		blocked bool
	}{
		{"bind source rejected", 400, "bind source path does not exist: SOURCE", true},
		{"unknown invalid request", 400, "probe rejected", false},
		{"proxy rejection", 403, "probe rejected", false},
		{"sentinel absent", 404, "No such image", false},
		{"unknown daemon response", 500, "probe rejected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mappingTempDir(t)
			for _, name := range []string{"sidecar", "entrypoint"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
					t.Fatal(err)
				}
			}
			var creates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(200)
					return
				}
				creates.Add(1)
				w.WriteHeader(tc.code)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": strings.ReplaceAll(tc.message, "SOURCE", root+"/sidecar")})
			}))
			defer server.Close()
			cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			p := &Provider{client: cli, cfg: Config{SidecarBinaryPath: root + "/sidecar", EntrypointPath: root + "/entrypoint"}, logger: quietLogger(), runtimeProbeGate: make(chan struct{}, 1)}
			p.preflightMandatoryBinds(context.Background())
			if err := p.runtimeAvailable(context.Background()); (err != nil) != tc.blocked {
				t.Fatalf("start preflight blocked=%v, want %v: %v", err != nil, tc.blocked, err)
			}
			if err := p.RuntimeReady(context.Background()); (err != nil) != tc.blocked {
				t.Fatalf("readiness blocked=%v, want %v: %v", err != nil, tc.blocked, err)
			}
			if tc.blocked {
				if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew"}); err == nil {
					t.Fatal("recognized bind rejection accepted crew start")
				}
			}
			if tc.code != 404 && !tc.blocked && creates.Load() < 3 {
				t.Fatal("inconclusive probe was cached as verified success")
			}
		})
	}
}

func TestDaemonMappingRejectsAmbiguousRootsAndReverseAliases(t *testing.T) {
	root := mappingTempDir(t)
	file := filepath.Join(root, "sidecar")
	if err := os.WriteFile(file, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(mappingTempDir(t), "root")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	bad := &daemonPathMap{local: alias, daemon: "/daemon/data"}
	if _, err := bad.translate(alias + "/sidecar"); err == nil {
		t.Fatal("symlink storage root accepted")
	}
	p := &Provider{pathMap: &daemonPathMap{local: root, daemon: "/daemon/data"}}
	if got, err := p.localBindSource("/daemon/data/sidecar"); err != nil || got != file {
		t.Fatalf("reverse=%q,%v", got, err)
	}
	if _, err := p.localBindSource("/daemon/data-other/sidecar"); err == nil {
		t.Fatal("reverse prefix collision accepted")
	}
	for _, cfg := range []Config{{OutputBasePath: root, DaemonDataPath: "relative"}, {OutputBasePath: root, DaemonDataPath: "/"}, {OutputBasePath: root, DaemonDataPath: "/daemon", ServerContainer: "server"}} {
		p.cfg = cfg
		if _, err := p.resolveDaemonPaths(context.Background()); err == nil {
			t.Fatalf("invalid configuration accepted: %+v", cfg)
		}
	}
}

func mappingTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRuntimeFileModePreservesWindows(t *testing.T) {
	for _, tc := range []struct {
		mode os.FileMode
		host string
		want bool
	}{
		{0666, "windows", true}, {0444, "windows", true}, {0644, "linux", false}, {0755, "linux", true}, {os.ModeDir | 0755, "windows", false}, {os.ModeDir | 0755, "linux", false},
	} {
		if got := runtimeFileModeAllowed(tc.mode, tc.host); got != tc.want {
			t.Errorf("mode=%v host=%s accepted=%v", tc.mode, tc.host, got)
		}
	}
}

func TestMappedControllerDoesNotQualifyForHostCapture(t *testing.T) {
	p := &Provider{detected: DetectResult{Host: "unix:///var/run/docker.sock"}, pathMap: &daemonPathMap{local: "/local", daemon: "/daemon"}}
	if p.SupportsHostCapture() {
		t.Fatal("mapped controller qualified without executor namespace handoff")
	}
}

func TestIdentityMappingKeepsRuntimeContract(t *testing.T) {
	root := mappingTempDir(t)
	cfg := Config{OutputBasePath: root, SidecarBinaryPath: root + "/sidecar", EntrypointPath: root + "/entrypoint"}
	for _, file := range []string{cfg.SidecarBinaryPath, cfg.EntrypointPath} {
		if err := os.WriteFile(file, []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
			t.Fatal(err)
		}
	}
	native := &Provider{cfg: cfg, logger: quietLogger()}
	mapped := &Provider{cfg: cfg, logger: quietLogger(), pathMap: &daemonPathMap{local: root, daemon: root}}
	if got, want := mapped.crewRuntimeContractDigest(), native.crewRuntimeContractDigest(); got == "" || got != want {
		t.Fatalf("identity mapping changed contract: %s -> %s", want, got)
	}
	if mapped.SupportsHostCapture() {
		t.Fatal("identity mapping must still retain mapped-controller capability restrictions")
	}
}

func TestMandatoryPreflightRecoversFromTransientFailure(t *testing.T) {
	root := mappingTempDir(t)
	for _, name := range []string{"sidecar", "entrypoint"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
			t.Fatal(err)
		}
	}
	var status atomic.Int32
	var creates atomic.Int32
	status.Store(503)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(200)
			return
		}
		creates.Add(1)
		w.WriteHeader(int(status.Load()))
		fmt.Fprint(w, `{"message":"temporary daemon failure"}`)
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	p := &Provider{client: cli, cfg: Config{SidecarBinaryPath: root + "/sidecar", EntrypointPath: root + "/entrypoint"}, logger: quietLogger(), runtimeProbeGate: make(chan struct{}, 1)}
	// A cancelled startup probe must not permanently disable the provider.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	p.preflightMandatoryBinds(cancelled)
	if p.runtimeErr != nil {
		t.Fatalf("transient failure became permanent: %v", p.runtimeErr)
	}
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("unknown create response blocked healthy daemon: %v", err)
	}
	status.Store(403)
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("inconclusive proxy response blocked healthy daemon: %v", err)
	}
	status.Store(404) // expected sentinel-image absence after mounts validate
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("provider did not recover without restart: %v", err)
	}
	before := creates.Load()
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if creates.Load() != before {
		t.Fatal("successful mandatory probes were not cached")
	}
}

func TestMandatoryPreflightGateHonorsRequestCancellation(t *testing.T) {
	p := &Provider{runtimeProbeGate: make(chan struct{}, 1)}
	p.runtimeProbeGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.RuntimeReady(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting readiness ignored cancellation: %v", err)
	}
	<-p.runtimeProbeGate
}

func TestFailedMappingDoesNotQualifyHostCapture(t *testing.T) {
	for _, cfg := range []Config{{ServerContainer: "server"}, {DaemonDataPath: "/daemon"}} {
		p := &Provider{cfg: cfg, detected: DetectResult{Host: "unix:///var/run/docker.sock"}, runtimeErr: errors.New("mapping discovery failed")}
		if p.SupportsHostCapture() {
			t.Fatal("failed mapping qualified a containerized controller")
		}
	}
}

func TestInconclusivePreflightPreservesRunningCrew(t *testing.T) {
	cfg := covRTConfig(t)
	for _, name := range []string{"sidecar", "entrypoint"} {
		if err := os.WriteFile(filepath.Join(cfg.OutputBasePath, name), []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfg.SidecarBinaryPath = filepath.Join(cfg.OutputBasePath, "sidecar")
	cfg.EntrypointPath = filepath.Join(cfg.OutputBasePath, "entrypoint")
	f := &covRT{inspectBody: `{"Id":"running-crew","State":{"Status":"running","Running":true}}`, createFn: func(req container.CreateRequest) (string, int) {
		if req.Config == nil || req.Config.Image != preflightSentinelImage {
			t.Error("inconclusive preflight triggered crew replacement")
		}
		return "", 500
	}}
	p := f.provider(t, cfg)
	p.runtimeProbeGate = make(chan struct{}, 1)
	p.setWarm("crew", "running-crew", "")
	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew"})
	if err != nil || id != "running-crew" {
		t.Fatalf("running crew not preserved: %s %v", id, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stops) != 0 || len(f.deletes) != 0 {
		t.Fatal("unknown probe reply stopped or removed a running crew")
	}
}

func TestInconclusiveProbeTimeoutDoesNotConsumeReadinessBudget(t *testing.T) {
	root := mappingTempDir(t)
	for _, name := range []string{"sidecar", "entrypoint"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(200)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(release); server.Close() }()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	p := &Provider{client: cli, cfg: Config{SidecarBinaryPath: root + "/sidecar", EntrypointPath: root + "/entrypoint"}, logger: quietLogger(), runtimeProbeGate: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.RuntimeReady(ctx); err != nil {
		t.Fatalf("unknown probe timeout blocked a pingable daemon: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("probe consumed the readiness DB/state budget")
	}
}
