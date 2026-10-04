//go:build linux

package docker

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const lifecycleImage = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// The fake daemon exercises create, inspect, audit and attached ready control.
type qualifiedFixture struct {
	p                               *Provider
	crew                            provider.CrewConfig
	c                               container.InspectResponse
	tagID                           string
	exists                          bool
	deletes, starts, runtimeCreates int
	helperCreates                   int
	stops                           int
	removeForce, removeVolumes      bool
	challenge                       string
}

// Runtime principals cannot supply the trusted host material needed by the
// positive lifecycle fixture. Exercise that admission boundary on those hosts,
// with a distinct scenario name, instead of reporting positive cases as skipped.
func lifecycleHostSupported(t *testing.T) bool {
	t.Helper()
	if os.Geteuid() != 1001 && os.Geteuid() != 1002 {
		return true
	}
	t.Run("unsupported-server-uid", func(t *testing.T) {
		p := &Provider{cfg: Config{OutputBasePath: t.TempDir(), InstanceID: "lifecycle-installation"}}
		source := filepath.Join(p.cfg.OutputBasePath, "bootstrap")
		if e := os.WriteFile(source, []byte("host material"), 0555); e != nil {
			t.Fatal(e)
		}
		if _, e := p.stageTrustedFile(source, 1024); !errors.Is(e, errStagedDenied) {
			t.Fatalf("runtime UID%d trusted artifact admission: %v", os.Geteuid(), e)
		}
		c := container.InspectResponse{Config: &container.Config{Labels: map[string]string{
			resourcelifecycle.InstanceLabel: p.cfg.InstanceID,
			crewCrewIDLabel:                 "crew1",
			stagedEnvLabel:                  newStagedEnvHandle(),
		}}}
		c.ID, c.Image = "runtime", lifecycleImage
		if scope, handle, e := p.stagedEnvScope(c); !errors.Is(e, errStagedDenied) || scope != "" || handle != "" {
			t.Fatalf("runtime UID%d secret scope admission: scope=%q handle=%q err=%v", os.Geteuid(), scope, handle, e)
		}
		if e := p.saveStagedEnv(c, []string{"TOKEN=synthetic"}); !errors.Is(e, errStagedDenied) {
			t.Fatalf("runtime UID%d secret publication: %v", os.Geteuid(), e)
		}
		if _, e := p.loadStagedEnv(c); !errors.Is(e, errStagedDenied) {
			t.Fatalf("runtime UID%d secret read: %v", os.Geteuid(), e)
		}
		if e := p.removeStagedEnv(c); !errors.Is(e, errStagedDenied) {
			t.Fatalf("runtime UID%d secret cleanup: %v", os.Geteuid(), e)
		}
		if _, e := os.Lstat(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName)); !os.IsNotExist(e) {
			t.Fatalf("denied admission created host material: %v", e)
		}
		t.Logf("UID%d admission denial verified; positive lifecycle scenarios require a supported host UID", os.Geteuid())
	})
	return false
}

func newQualifiedFixture(t *testing.T, existing bool) *qualifiedFixture {
	t.Helper()
	cfg := covRTConfig(t)
	cfg.InstanceID = "lifecycle-installation"
	cfg.SidecarBinaryPath = filepath.Join(cfg.OutputBasePath, "sidecar")
	cfg.EntrypointPath = filepath.Join(cfg.OutputBasePath, "bootstrap")
	raw := make([]byte, 64)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	if e := os.WriteFile(cfg.SidecarBinaryPath, raw, 0555); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(cfg.EntrypointPath, []byte("#!/bin/sh\nexit 0\n"), 0555); e != nil {
		t.Fatal(e)
	}
	f := &qualifiedFixture{tagID: lifecycleImage, exists: existing, crew: provider.CrewConfig{ID: "crew1", Slug: "alpha", Image: "crewship-cache:pilot", NetworkMode: "restricted"}}
	cfg.EgressFenceCrews = []string{f.crew.ID}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	cli, e := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.43"))
	if e != nil {
		srv.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cli.Close(); srv.Close() })
	p := &Provider{client: cli, cfg: cfg, logger: quietLogger()}
	f.p = p
	covCrewBindDirs(t, cfg)
	if existing {
		c, h, _, e := p.buildStagedCrewContainerConfig(t.Context(), f.crew, p.CrewContainerName(f.crew.ID, f.crew.Slug), f.crew.Image, "runc", 8192, 2, crewDirs{})
		if e != nil {
			t.Fatal(e)
		}
		c.Labels[stagedEnvLabel] = newStagedEnvHandle()
		f.capture(c, h)
		f.c.State = &container.State{Status: "running", Running: true, StartedAt: "2026-10-04T00:00:00Z"}
		if e := p.saveStagedEnv(f.c, []string{"TOKEN=synthetic"}); e != nil {
			t.Fatal(e)
		}
	}
	return f
}
func (f *qualifiedFixture) capture(c *container.Config, h *container.HostConfig) {
	f.c = container.InspectResponse{Config: c, HostConfig: h}
	f.c.ID = strings.Repeat("a", 64)
	f.c.Image = c.Image
	f.c.State = &container.State{Status: "created"}
	for _, m := range h.Mounts {
		f.c.Mounts = append(f.c.Mounts, container.MountPoint{Type: m.Type, Source: m.Source, Destination: m.Target, RW: !m.ReadOnly})
	}
}
func (f *qualifiedFixture) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": f.tagID, "Architecture": "amd64", "Config": map[string]any{"Env": []string{"PATH=/usr/bin:/bin"}}})
	case strings.HasSuffix(path, "/containers/json"):
		items := []map[string]any{}
		if f.exists {
			items = append(items, map[string]any{"Id": f.c.ID, "Names": []string{"/" + f.p.CrewContainerName(f.crew.ID, f.crew.Slug)}, "State": f.c.State.Status})
		}
		_ = json.NewEncoder(w).Encode(items)
	case strings.HasSuffix(path, "/containers/create"):
		var req container.CreateRequest
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			panic(e)
		}
		id := "trusted-helper"
		if req.Labels[fenceHelperLabel] == "true" {
			f.helperCreates++
		}
		if req.Labels[crewKindLabel] == crewRuntimeKind {
			if f.exists {
				http.Error(w, `{"message":"existing runtime blocks retry"}`, 409)
				return
			}
			f.runtimeCreates++
			f.capture(req.Config, req.HostConfig)
			f.exists = true
			id = f.c.ID

		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
		if !f.exists {
			http.Error(w, `{"message":"not found"}`, 404)
			return
		}
		c := f.c
		_ = json.NewEncoder(w).Encode(c)
	case strings.HasSuffix(path, "/archive"):
		raw, _ := json.Marshal(map[string]any{"name": "parent", "mode": uint32(os.ModeDir | 0755), "mtime": "2026-10-04T00:00:00Z"})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(raw))
	case strings.HasSuffix(path, "/exec"):
		var req container.ExecCreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		id := "status-exec"
		switch {
		case len(req.Cmd) == 5 && req.Cmd[1] == "--staged-control" && req.Cmd[2] == "status":
			f.challenge = req.Cmd[4]
		case len(req.Cmd) == 2 && req.Cmd[1] == "--version":
			id = "version-exec"
		case len(req.Cmd) > 2 && req.Cmd[1] == "--staged-exec":
			id = "hook-exec"
		default:
			panic(fmt.Sprintf("unexpected exec: %v", req.Cmd))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/start"):
		var req container.ExecStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Detach {
			w.WriteHeader(200)
			return
		}
		conn, buf, e := w.(http.Hijacker).Hijack()
		if e != nil {
			panic(e)
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		raw, _ := json.Marshal(stagedstart.Status{Challenge: f.challenge, Nonce: strings.Repeat("a", 32), Phase: "ready"})
		if !strings.Contains(path, "status-exec") {
			raw = nil
		}
		hdr := make([]byte, 8)
		hdr[0] = 1
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(raw)))
		_, _ = buf.Write(hdr)
		_, _ = buf.Write(raw)
		_ = buf.Flush()
	case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
		_, _ = io.WriteString(w, `{"Running":false,"ExitCode":0}`)
	case r.Method == "DELETE" && strings.Contains(path, "/containers/"):
		id := path[strings.LastIndex(path, "/")+1:]
		if id == f.c.ID {
			f.deletes++
			f.removeForce = r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
			f.removeVolumes = r.URL.Query().Get("v") == "1" || r.URL.Query().Get("v") == "true"
			f.exists = false
		}
		w.WriteHeader(204)
	case strings.HasSuffix(path, "/start"):
		if f.c.ID != "" && strings.Contains(path, f.c.ID) {
			f.starts++
			f.c.State.Status = "running"
			f.c.State.Running = true
			f.c.State.StartedAt = fmt.Sprintf("2026-10-04T00:00:%02dZ", f.starts)
		}
		w.WriteHeader(204)
	case strings.HasSuffix(path, "/stop"):
		f.stops++
		f.c.State = &container.State{Status: "exited"}
		w.WriteHeader(204)
	case strings.HasSuffix(path, "/wait"):
		_, _ = io.WriteString(w, `{"StatusCode":0}`)
	case strings.HasSuffix(path, "/logs"):
	case strings.HasSuffix(path, "/networks"):
		_, _ = io.WriteString(w, `[{"Name":"crewship","Id":"network"}]`)
	case strings.HasSuffix(path, "/volumes"):
		_, _ = io.WriteString(w, `{"Volumes":[]}`)
	case strings.HasSuffix(path, "/volumes/create"):
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"Name":"volume"}`)
	case strings.Contains(path, "/volumes/"):
		http.Error(w, `{"message":"not found"}`, 404)
	default:
		w.WriteHeader(200)
	}
}
