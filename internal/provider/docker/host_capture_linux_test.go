//go:build linux

package docker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

func TestHostCaptureRejectsExposedSpoolBeforeAuthority(t *testing.T) {
	for _, source := range []string{"/", "/private", "/private/run"} {
		t.Run(source, func(t *testing.T) {
			p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/containers/cid/json") {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(hostCaptureInspect("start", []map[string]any{{"Source": source}}))
			})
			p.detected.Host = "unix:///var/run/docker.sock"
			_, _, err := p.prepareSupportedHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
			if err == nil || !strings.Contains(err.Error(), "exposed") {
				t.Fatalf("spool ancestor mount admission=%v", err)
			}
		})
	}
}

func TestHostCaptureAdmissionBindsConfirmedFenceStart(t *testing.T) {
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
	_, _, err := p.prepareSupportedHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
	if err == nil || !strings.Contains(err.Error(), "fence") {
		t.Fatalf("restart between fence guard and authority snapshot admission=%v", err)
	}
}

func TestHostCaptureAuthorityRetainsConfirmedContainerStart(t *testing.T) {
	p := newCovProvider(t, Config{EgressFenceCrews: []string{"crew"}}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(hostCaptureInspect("confirmed", nil))
	})
	p.detected.Host = "unix:///var/run/docker.sock"
	p.fenced.Store("cid", "confirmed")
	p.fencedCrew.Store("cid", "crew")
	p.fenceTeams.Store("crew", provider.CrewConfig{ID: "crew", Slug: "slug"})
	endpoint, data, err := p.prepareSupportedHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, "/private/run", []string{"stop"})
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
	_, _, err := p.prepareSupportedHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, filepath.Join(protected, "run"), []string{"stop"})
	if err == nil {
		t.Fatal("read-only bind alias exposing host spool admitted")
	}
}

func TestHostCaptureRejectsBindBackedVolumeExposingSpool(t *testing.T) {
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
	_, _, err := p.prepareSupportedHostCapture(t.Context(), provider.ExecConfig{ContainerID: "cid", User: "1001:1001"}, filepath.Join(protected, "run"), []string{"stop"})
	if err == nil {
		t.Fatal("bind-backed named volume exposing host spool admitted")
	}
}
