package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
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
		{"named volume", 200, []map[string]any{{"Type": "volume", "Source": "/docker/volume/_data", "Destination": "/data", "RW": true}}, false},
		{"bind", 200, []map[string]any{{"Type": "bind", "Source": "/host/data", "Destination": "/data", "RW": true}}, false},
		{"readonly parent first", 200, []map[string]any{{"Type": "bind", "Source": "/host/readonly", "Destination": "/data", "RW": false}, {"Type": "volume", "Source": "/docker/volume/storage", "Destination": "/data/storage", "RW": true}}, false},
		{"readonly parent last", 200, []map[string]any{{"Type": "volume", "Source": "/docker/volume/storage", "Destination": "/data/storage", "RW": true}, {"Type": "bind", "Source": "/host/readonly", "Destination": "/data", "RW": false}}, false},
		{"tmpfs ancestor", 200, []map[string]any{{"Type": "tmpfs", "Destination": "/data", "RW": true}, {"Type": "volume", "Source": "/docker/volume/storage", "Destination": "/data/storage", "RW": true}}, false},
		{"missing mapping", 200, nil, true}, {"proxy denied", 403, nil, true}, {"daemon error", 500, nil, true},
		{"readonly", 200, []map[string]any{{"Type": "volume", "Source": "/docker/data", "Destination": "/data", "RW": false}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/containers/server/json") {
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"Mounts": tc.mounts})
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

func TestMandatoryBindPreflightRetainsFailure(t *testing.T) {
	for _, code := range []int{400, 403, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			root := mappingTempDir(t)
			for _, name := range []string{"sidecar", "entrypoint"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, `{"message":"probe rejected"}`)
			}))
			defer server.Close()
			cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			p := &Provider{client: cli, cfg: Config{SidecarBinaryPath: root + "/sidecar", EntrypointPath: root + "/entrypoint"}, logger: quietLogger()}
			p.preflightMandatoryBinds(context.Background())
			if (p.runtimeErr != nil) != (code != 404) {
				t.Fatalf("status %d: runtime error %v", code, p.runtimeErr)
			}
			if code != 404 {
				if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew"}); err == nil {
					t.Fatal("unavailable runtime accepted crew start")
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
