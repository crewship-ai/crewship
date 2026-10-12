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

func TestHostCapturePrepareRequiresSupportedControllerTransport(t *testing.T) {
	for _, endpoint := range []string{"unix:///var/run/docker.sock", "tcp://daemon:2375"} {
		t.Run(endpoint, func(t *testing.T) {
			var calls atomic.Int32
			p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(hostCaptureInspect("start", nil))
			})
			p.detected.Host = endpoint
			_, authority, err := p.PrepareHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
			supported := runtime.GOOS == "linux" && hostCaptureControllerUIDSupported(os.Geteuid()) && strings.HasPrefix(endpoint, "unix://")
			if supported {
				if err != nil || len(authority) == 0 || calls.Load() == 0 {
					t.Fatalf("supported admission=%v authority=%s calls=%d", err, authority, calls.Load())
				}
			} else if err == nil || len(authority) != 0 || calls.Load() != 0 {
				t.Fatalf("unsupported controller crossed API boundary: err=%v authority=%s calls=%d", err, authority, calls.Load())
			}
		})
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
