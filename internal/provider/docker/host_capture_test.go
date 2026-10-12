package docker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

func hostCaptureInspect(start string, mounts []map[string]any) map[string]any {
	return map[string]any{"Id": "cid", "Image": "sha256:immutable", "State": map[string]any{"Running": true, "StartedAt": start}, "Config": map[string]any{"Labels": map[string]string{crewCrewIDLabel: "crew", crewCrewLabel: "slug"}}, "Mounts": mounts}
}

func TestHostCaptureRejectsExposedSpoolBeforeAuthority(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host capture requires local Linux Docker")
	}
	for _, source := range []string{"/", "/private", "/private/run"} {
		t.Run(source, func(t *testing.T) {
			p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/containers/cid/json") {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(hostCaptureInspect("start", []map[string]any{{"Source": source}}))
			})
			p.detected.Host = "unix:///var/run/docker.sock"
			_, _, err := p.PrepareHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
			if err == nil || !strings.Contains(err.Error(), "exposed") {
				t.Fatalf("spool ancestor mount admission=%v", err)
			}
		})
	}
}

func TestHostCaptureAdmissionBindsConfirmedFenceStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host capture requires local Linux Docker")
	}
	var calls atomic.Int32
	p := newCovProvider(t, Config{EgressFenceCrews: []string{"crew"}}, func(w http.ResponseWriter, r *http.Request) {
		start := "confirmed"
		if calls.Add(1) > 1 {
			start = "unconfirmed-restart"
		}
		_ = json.NewEncoder(w).Encode(hostCaptureInspect(start, nil))
	})
	p.detected.Host = "unix:///var/run/docker.sock"
	p.fenced.Store("cid", "confirmed")
	p.fencedCrew.Store("cid", "crew")
	p.fenceTeams.Store("crew", provider.CrewConfig{ID: "crew", Slug: "slug"})
	_, _, err := p.PrepareHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
	if err == nil || !strings.Contains(err.Error(), "fence") {
		t.Fatalf("restart between fence guard and authority snapshot admission=%v", err)
	}
}

func TestHostCaptureAuthorityRejectsAmbiguousOrMismatchedInput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	valid := hostCaptureAuthority{Version: 1, ContainerID: "cid", ImageID: "image", StartedAt: "start", Directory: dir, StopArgs: []string{"stop"}}
	data, _ := json.Marshal(valid)
	for _, change := range []string{"unknown", "trailing", "container", "directory", "version"} {
		t.Run(change, func(t *testing.T) {
			input := runoutput.HostInput{Authority: data, Directory: dir, Endpoint: "unix:///var/run/docker.sock", Command: runoutput.HostCommand{ContainerID: "cid"}}
			switch change {
			case "unknown":
				input.Authority = append(append([]byte{}, data[:len(data)-1]...), []byte(`,"untrusted":true}`)...)
			case "trailing":
				input.Authority = append(append([]byte{}, data...), []byte(` {}`)...)
			case "container":
				input.Command.ContainerID = "foreign"
			case "directory":
				input.Directory = dir + "-foreign"
			case "version":
				input.Authority = []byte(strings.Replace(string(data), `"version":1`, `"version":2`, 1))
			}
			executor, closer, err := NewHostCaptureExecutor(input)
			if closer != nil {
				defer closer.Close()
			}
			if err == nil || executor != nil {
				t.Fatal("ambiguous authority admitted")
			}
		})
	}
}

func TestHostCaptureStartRefusesChangedContainerBeforeExecCreate(t *testing.T) {
	var creates atomic.Int32
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exec") {
			creates.Add(1)
		}
		_ = json.NewEncoder(w).Encode(hostCaptureInspect("restarted", nil))
	})
	executor := &hostCaptureExecutor{provider: p, authority: hostCaptureAuthority{ContainerID: "cid", ImageID: "sha256:immutable", StartedAt: "original", Directory: "/private/run"}}
	_, err := executor.Start(t.Context(), runoutput.HostCommand{ContainerID: "cid", User: "1001:1001", Args: []string{"run"}})
	if err == nil || creates.Load() != 0 {
		t.Fatalf("changed container started: err=%v creates=%d", err, creates.Load())
	}
}

func TestHostCaptureAuthorityRetainsConfirmedContainerStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host capture requires local Linux Docker")
	}
	p := newCovProvider(t, Config{EgressFenceCrews: []string{"crew"}}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(hostCaptureInspect("confirmed", nil))
	})
	p.detected.Host = "unix:///var/run/docker.sock"
	p.fenced.Store("cid", "confirmed")
	p.fencedCrew.Store("cid", "crew")
	p.fenceTeams.Store("crew", provider.CrewConfig{ID: "crew", Slug: "slug"})
	endpoint, data, err := p.PrepareHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
	if err != nil {
		t.Fatal(err)
	}
	var authority hostCaptureAuthority
	if err = json.Unmarshal(data, &authority); err != nil {
		t.Fatal(err)
	}
	if endpoint != p.detected.Host || !authority.Fence || authority.ContainerID != "cid" || authority.StartedAt != "confirmed" || authority.ImageID != "sha256:immutable" {
		t.Fatalf("authority=%+v endpoint=%q", authority, endpoint)
	}
	executor, closer, err := NewHostCaptureExecutor(runoutput.HostInput{Authority: data, Directory: "/private/run", Endpoint: endpoint, Command: runoutput.HostCommand{ContainerID: "cid"}})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	adapter := executor.(*hostCaptureExecutor)
	if got, ok := adapter.provider.fenced.Load("cid"); !ok || got != "confirmed" {
		t.Fatalf("helper fence snapshot=%v present=%v", got, ok)
	}
}

func TestHostCaptureRejectsBindAliasExposingSpoolAncestor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host capture requires local Linux Docker")
	}
	root := t.TempDir()
	protected := filepath.Join(root, "protected")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, alias); err != nil {
		t.Fatal(err)
	}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(hostCaptureInspect("start", []map[string]any{{"Type": "bind", "Source": alias, "Destination": "/visible", "RW": false}}))
	})
	p.detected.Host = "unix:///var/run/docker.sock"
	_, _, err := p.PrepareHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, filepath.Join(protected, "run"), []string{"stop"})
	if err == nil {
		t.Fatal("read-only bind alias exposing host spool admitted")
	}
}

func TestHostCaptureRejectsBindBackedVolumeExposingSpool(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host capture requires local Linux Docker")
	}
	root := t.TempDir()
	protected := filepath.Join(root, "protected")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, alias); err != nil {
		t.Fatal(err)
	}
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/volumes/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "owned", "Driver": "local", "Mountpoint": "/var/lib/docker/volumes/owned/_data", "Options": map[string]string{"type": "none", "o": "bind,nodev,noexec", "device": alias}})
			return
		}
		_ = json.NewEncoder(w).Encode(hostCaptureInspect("start", []map[string]any{{"Type": "volume", "Name": "owned", "Source": "/var/lib/docker/volumes/owned/_data", "Destination": "/visible"}}))
	})
	p.detected.Host = "unix:///var/run/docker.sock"
	_, _, err := p.PrepareHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, filepath.Join(protected, "run"), []string{"stop"})
	if err == nil {
		t.Fatal("bind-backed named volume exposing host spool admitted")
	}
}

func TestHostCaptureStopRefusesRestartBeforeSignal(t *testing.T) {
	var creates atomic.Int32
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/exec/eid/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ID": "eid", "ContainerID": "cid", "Running": true, "Pid": 1234})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/exec") {
			creates.Add(1)
		}
		_ = json.NewEncoder(w).Encode(hostCaptureInspect("restarted", nil))
	})
	executor := &hostCaptureExecutor{provider: p, execID: "eid", authority: hostCaptureAuthority{ContainerID: "cid", ImageID: "sha256:immutable", StartedAt: "original", Directory: "/private/run"}}
	if err := executor.Stop(t.Context(), "eid"); err == nil {
		t.Fatal("restart admitted into process signal")
	}
	if creates.Load() != 0 {
		t.Fatal("stop helper launched into restarted container")
	}
}

func TestHostCaptureRestoreRejectsForeignExecIdentity(t *testing.T) {
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.43")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ID": "foreign", "ContainerID": "another-container", "Running": true, "Pid": 1234})
	})
	a := hostCaptureAuthority{Version: 1, ContainerID: "cid", ImageID: "image", StartedAt: "start", Directory: "/private/run", StopArgs: []string{"stop"}}
	data, _ := json.Marshal(a)
	input := runoutput.HostInput{Authority: data, Directory: a.Directory, Endpoint: p.client.DaemonHost(), Command: runoutput.HostCommand{ContainerID: "cid"}}
	executor, closer, err := p.RestoreHostCapture(input, "foreign")
	if closer != nil {
		defer closer.Close()
	}
	if err == nil || executor != nil {
		t.Fatal("foreign provider execution restored under owned identity")
	}
}

func TestHostCaptureControllerUIDSupport(t *testing.T) {
	for _, tc := range []struct {
		uid       int
		supported bool
	}{{0, true}, {1000, true}, {1001, false}, {1002, false}, {65534, true}} {
		if got := hostCaptureControllerUIDSupported(tc.uid); got != tc.supported {
			t.Errorf("UID%d supported=%v want=%v", tc.uid, got, tc.supported)
		}
	}
}
