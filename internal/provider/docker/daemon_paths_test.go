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
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
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

func TestMappingRequiresResolvableReferences(t *testing.T) {
	root := mappingTempDir(t)
	if err := os.Symlink(filepath.Join(root, "pending"), filepath.Join(root, "reference")); err != nil {
		t.Fatal(err)
	}
	mapping := &daemonPathMap{local: root, daemon: "/daemon/storage"}
	for _, path := range []string{"reference", "reference/child", "reference/child/deeper"} {
		if _, err := mapping.translate(filepath.Join(root, path)); err == nil {
			t.Errorf("unresolved reference accepted: %s", path)
		}
	}
	if got, err := mapping.translate(filepath.Join(root, "new", "child")); err != nil || got != "/daemon/storage/new/child" {
		t.Fatalf("ordinary missing suffix: %q, %v", got, err)
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
		name     string
		code     int
		message  string
		blocked  bool
		verified bool
	}{
		{"bind source rejected", 400, "bind source path does not exist: SOURCE", true, false},
		{"unknown invalid request", 400, "probe rejected", false, false},
		{"proxy rejection", 403, "probe rejected", false, false},
		{"sentinel absent", 404, "No such image: " + preflightSentinelImage, false, true},
		{"unknown not found", 404, "Not Found", false, false},
		{"unrelated image absent", 404, "No such image: unrelated", false, false},
		{"different sentinel image", 404, "No such image: " + preflightSentinelImage + "-other", false, false},
		{"unknown daemon response", 500, "probe rejected", false, false},
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
				w.Header().Set("Content-Type", "application/json")
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
			if !tc.verified && !tc.blocked {
				if creates.Load() < 3 {
					t.Error("inconclusive probe was cached as verified success")
				}
			}
			if !tc.blocked {
				known := errors.New("previous confirmed bind rejection")
				p.runtimeProbeState.Store(&mandatoryProbeState{err: known})
				got := p.RuntimeReady(context.Background())
				if tc.verified && got != nil {
					t.Errorf("verified success retained prior rejection: %v", got)
				} else if !tc.verified && !errors.Is(got, known) {
					t.Errorf("inconclusive reply cleared confirmed rejection: %v", got)
				}
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(status.Load()))
		message := "temporary daemon failure"
		if status.Load() == 404 {
			message = "No such image: " + preflightSentinelImage
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
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

func TestDaemonPathChecksPlainVolumeOverlap(t *testing.T) {
	root := mappingTempDir(t)
	protected := filepath.Join(root, "agent-cli")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	const daemonRoot = "/var/lib/docker/volumes/server-data/_data"
	for _, tc := range []struct {
		name       string
		source     string
		mountpoint string
		wantError  bool
	}{
		{name: "inspect source aliases server data", source: daemonRoot, mountpoint: "/var/lib/docker/volumes/other/_data", wantError: true},
		{name: "volume mountpoint aliases server data", source: "/unrelated", mountpoint: daemonRoot, wantError: true},
		{name: "unrelated plain volume", source: "/var/lib/docker/volumes/other/_data", mountpoint: "/var/lib/docker/volumes/other/_data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"Name": "fixture", "Driver": "local", "Mountpoint": tc.mountpoint})
			})
			defer close()
			p.pathMap = &daemonPathMap{local: root, daemon: daemonRoot}
			mounts := []container.MountPoint{{Type: mount.TypeVolume, Name: "fixture", Source: tc.source, RW: true}}
			if err := p.hostMountTreeHidden(t.Context(), mounts, protected, true); (err != nil) != tc.wantError {
				t.Fatalf("mount qualification = %v, want error %t", err, tc.wantError)
			}
		})
	}
}

func TestDaemonPathChecksRefuseUnavailableConfiguredMapping(t *testing.T) {
	root := mappingTempDir(t)
	p := &Provider{cfg: Config{ServerContainer: "fixture"}}
	if p.matchesDaemonPath(root, root) {
		t.Fatal("accepted a local path without the configured daemon mapping")
	}
	if _, err := p.localBindSource(root); err == nil {
		t.Fatal("resolved an inspected source without the configured daemon mapping")
	}
	if err := p.hostMountTreeHidden(t.Context(), nil, root, true); err == nil {
		t.Fatal("qualified the protected path without the configured daemon mapping")
	}
}

func TestMappedFeatureVolumeAdmissionPrecedesContainerStart(t *testing.T) {
	for _, tc := range []struct {
		name, destination, volume, mountpoint string
		driver                                string
		status                                int
		allowed                               bool
	}{
		{name: "own home", destination: "/home/agent", volume: "crewship-home-slug-own", mountpoint: "/daemon/volumes/own/_data", driver: "local", status: 200, allowed: true},
		{name: "server private volume", destination: "/feature", volume: "server-db", mountpoint: "/daemon/volumes/db/_data", driver: "local", status: 200},
		{name: "other crew home", destination: "/home/agent", volume: "crewship-home-slug-other", mountpoint: "/daemon/volumes/other/_data", driver: "local", status: 200},
		{name: "mapped storage alias", destination: "/home/agent", volume: "crewship-home-slug-own", mountpoint: "/daemon/volumes/storage/_data", driver: "local", status: 200},
		{name: "driver plugin", destination: "/home/agent", volume: "crewship-home-slug-own", mountpoint: "/daemon/volumes/own/_data", driver: "plugin", status: 200},
		{name: "uninspectable at start", destination: "/home/agent", volume: "crewship-home-slug-own", mountpoint: "/daemon/volumes/own/_data", driver: "local", status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var starts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/volumes/"+tc.volume):
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"Name": tc.volume, "Driver": tc.driver, "Mountpoint": tc.mountpoint})
				case strings.HasSuffix(r.URL.Path, "/containers/crew/json"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Mounts": []container.MountPoint{{
						Type: mount.TypeVolume, Destination: tc.destination, Name: tc.volume, Source: tc.mountpoint,
					}}})
				case strings.HasSuffix(r.URL.Path, "/containers/crew/start"):
					starts.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cli.Close() })
			p := &Provider{client: cli, pathMap: &daemonPathMap{local: "/app/storage", daemon: "/daemon/volumes/storage/_data"}}
			team := provider.CrewConfig{ID: "own", Slug: "slug"}
			err = p.startMappedCrewContainer(t.Context(), "crew", team)
			if got := err == nil; got != tc.allowed || (starts.Load() == 1) != tc.allowed {
				t.Fatalf("allowed=%v starts=%d err=%v, want allowed=%v", got, starts.Load(), err, tc.allowed)
			}
			team.ExtraMounts = []provider.CrewMount{{Source: "feature", Type: "volume", Target: "/extra"}}
			if err := p.checkMappedFeatureVolumes(team); err == nil {
				t.Fatal("mapped runtime must reject feature-declared volume")
			}
			p.pathMap = nil
			if err := p.checkMappedFeatureVolumes(team); err != nil {
				t.Fatalf("unmapped runtime changed its feature rules: %v", err)
			}
		})
	}
}

func TestMappedRuntimeMountAdmissionProtectsOwnedRuntime(t *testing.T) {
	root := mappingTempDir(t)
	sidecar := filepath.Join(root, "sidecar")
	if err := os.WriteFile(sidecar, []byte("runtime fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var updates, stops atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/crew/update"):
			var options container.UpdateConfig
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil || options.RestartPolicy.Name != container.RestartPolicyDisabled {
				t.Errorf("restart policy update: %v, %v", options.RestartPolicy, err)
			}
			updates.Add(1)
			_, _ = fmt.Fprint(w, "{}")
		case strings.HasSuffix(r.URL.Path, "/containers/crew/stop"):
			if updates.Load() != 1 {
				t.Error("runtime stopped before disabling restart")
			}
			stops.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	p := &Provider{client: cli, pathMap: &daemonPathMap{local: root, daemon: "/daemon/storage"}}
	p.cfg.InstanceID = "own-installation"
	p.cfg.SidecarBinaryPath = sidecar
	team := provider.CrewConfig{ID: "own", Slug: "slug"}
	mounts := []container.MountPoint{{Type: mount.TypeBind, Source: "/daemon/storage/sidecar", Destination: "/usr/local/bin/crewship-sidecar"}}
	if err := p.checkMappedRuntimeMounts(t.Context(), mounts, team); err != nil {
		t.Fatalf("legitimate read-only helper bind rejected: %v", err)
	}
	mounts[0].RW = true
	refusal := p.checkMappedRuntimeMounts(t.Context(), mounts, team)
	if !errors.Is(refusal, errMappedRuntimeMount) {
		t.Fatalf("writable helper accepted: %v", refusal)
	}
	inspected := container.InspectResponse{
		ID: "crew", State: &container.State{Running: true},
		Config: &container.Config{Labels: map[string]string{
			crewKindLabel: crewRuntimeKind, crewCrewIDLabel: team.ID,
			resourcelifecycle.InstanceLabel: "other-installation",
		}},
	}
	if err := p.stopMappedRuntimeOnRefusal(t.Context(), team, inspected, refusal); !errors.Is(err, refusal) {
		t.Fatalf("foreign runtime should still be refused: %v", err)
	}
	if updates.Load() != 0 || stops.Load() != 0 {
		t.Fatal("refusal altered foreign runtime")
	}
	inspected.Config.Labels[resourcelifecycle.InstanceLabel] = p.cfg.InstanceID
	if err := p.stopMappedRuntimeOnRefusal(t.Context(), team, inspected, refusal); !errors.Is(err, refusal) {
		t.Fatalf("missing original refusal: %v", err)
	}
	if updates.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("owned refusal produced updates=%d stops=%d", updates.Load(), stops.Load())
	}
}
