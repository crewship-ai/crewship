//go:build linux

package docker

import (
	"context"
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

// No real Docker or image program runs here. The fake daemon implements the
// actual create/inspect/audit/ready-reuse boundary, including attached status.
type lifecycleFixture struct {
	p                               *Provider
	crew                            provider.CrewConfig
	c                               container.InspectResponse
	tagID                           string
	fail                            string
	exists                          bool
	deletes, starts, runtimeCreates int
	imageExecs                      int
	removeForce, removeVolumes      bool
	challenge                       string
	faultDir                        string
	beforeFailure                   func()
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

func newLifecycleFixture(t *testing.T, existing bool) *lifecycleFixture {
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
	f := &lifecycleFixture{tagID: lifecycleImage, exists: existing, crew: provider.CrewConfig{ID: "crew1", Slug: "alpha", Image: "crewship-cache:pilot", NetworkMode: "restricted"}}
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
		c, h, e := p.buildCrewContainerConfig(t.Context(), f.crew, p.CrewContainerName(f.crew.ID, f.crew.Slug), f.crew.Image, "runc", 8192, 2, crewDirs{})
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
	if existing {
		if _, e := p.stagedControl(t.Context(), f.c.ID, "status", ""); e != nil {
			t.Fatalf("fake ready control invalid: %v", e)
		}
	}
	return f
}
func (f *lifecycleFixture) capture(c *container.Config, h *container.HostConfig) {
	f.c = container.InspectResponse{Config: c, HostConfig: h}
	f.c.ID = "runtime-created-0123456789"
	f.c.Image = c.Image
	f.c.State = &container.State{Status: "created"}
	for _, m := range h.Mounts {
		f.c.Mounts = append(f.c.Mounts, container.MountPoint{Type: m.Type, Source: m.Source, Destination: m.Target, RW: !m.ReadOnly})
	}
}
func (f *lifecycleFixture) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
		if strings.Contains(path, "unavailable") {
			http.Error(w, `{"message":"base tag unavailable"}`, 404)
			return
		}
		if f.fail == "image" {
			http.Error(w, `{"message":"image lookup injected"}`, 500)
			return
		}
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
		if req.Labels[crewKindLabel] == crewRuntimeKind {
			if f.exists {
				http.Error(w, `{"message":"existing runtime blocks retry"}`, 409)
				return
			}
			f.runtimeCreates++
			f.capture(req.Config, req.HostConfig)
			f.exists = true
			id = f.c.ID
			if f.fail == "save" {
				_ = os.Mkdir(filepath.Join(f.p.cfg.OutputBasePath, runtimestage.DirName, "staged-env"), 0755)
			}
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
		if !f.exists {
			http.Error(w, `{"message":"not found"}`, 404)
			return
		}
		if f.fail == "inspect" {
			if f.beforeFailure != nil {
				f.beforeFailure()
			}
			http.Error(w, `{"message":"inspect injected"}`, 500)
			return
		}
		if f.fail == "save" {
			// Refuse publication with an untrusted private directory.
			target, e := f.p.stagedEnvPath(f.c)
			if e == nil {
				_ = os.Chmod(filepath.Dir(target), 0755)
				f.faultDir = filepath.Dir(target)
			}
		}
		c := f.c
		if f.fail == "audit" || f.fail == "audit-remove" {
			c.HostConfig = &container.HostConfig{Privileged: true}
		}
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
			f.imageExecs++
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
			if f.fail == "audit-remove" || f.fail == "remove" {
				http.Error(w, `{"message":"remove injected"}`, 500)
				return
			}
			f.exists = false
		}
		w.WriteHeader(204)
	case strings.HasSuffix(path, "/start"):
		if f.c.ID != "" && strings.Contains(path, f.c.ID) {
			f.starts++
			f.c.State.Status = "running"
			f.c.State.Running = true
		}
		w.WriteHeader(204)
	case strings.HasSuffix(path, "/stop"):
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

func TestStagedLifecycleTagReuse(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	for _, mode := range []string{"cold", "warm", "stopped", "cached-image-wins", "changed-running", "changed-stopped", "retag-warm", "lookup-error", "empty-image-id", "mutable-config-bypass"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t, true)
			if mode == "stopped" || mode == "changed-stopped" {
				f.c.State = &container.State{Status: "exited"}
			}
			if mode == "cached-image-wins" {
				f.crew.CachedImage = f.crew.Image
				f.crew.Image = "unavailable:base"
			}
			if mode == "warm" || mode == "retag-warm" {
				f.p.setWarm(f.crew.ID, f.c.ID, f.crew.Image)
				f.p.fencedCrew.Store(f.c.ID, f.crew.ID)
			}
			if strings.HasPrefix(mode, "changed-") || mode == "retag-warm" || mode == "mutable-config-bypass" {
				f.tagID = "sha256:" + strings.Repeat("b", 64)
			}
			if mode == "mutable-config-bypass" {
				f.c.Config.Image = f.crew.Image
			}
			if mode == "empty-image-id" {
				f.tagID = ""
			}
			if mode == "lookup-error" {
				f.fail = "image"
			}
			id, e := f.p.EnsureCrewRuntime(t.Context(), f.crew)
			switch mode {
			case "changed-running", "retag-warm", "mutable-config-bypass":
				if !errors.Is(e, provider.ErrRuntimeImageUpdatePending) || f.deletes != 0 || f.starts != 0 {
					t.Fatalf("changed image was not pending without mutation: %v deletes=%d starts=%d", e, f.deletes, f.starts)
				}
			case "lookup-error", "empty-image-id":
				if e == nil || f.deletes != 0 || f.starts != 0 {
					t.Fatalf("lookup failed open: %v", e)
				}
			case "changed-stopped":
				if f.deletes != 1 {
					t.Fatalf("stopped image update was not recreated: %v deletes=%d", e, f.deletes)
				}
			default:
				if e != nil || id != f.c.ID || f.deletes != 0 || f.runtimeCreates != 0 {
					t.Fatalf("same image was not reused: id=%s err=%v deletes=%d creates=%d", id, e, f.deletes, f.runtimeCreates)
				}
			}
		})
	}
}

func TestStagedLifecycleFailedPreparation(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	for _, failure := range []string{"inspect", "inspect-cancel", "save", "audit", "audit-remove"} {
		t.Run(failure, func(t *testing.T) {
			f := newLifecycleFixture(t, false)
			f.fail = failure
			ctx := t.Context()
			if failure == "inspect-cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.fail = "inspect"
				f.beforeFailure = cancel
			}
			_, e := f.p.EnsureCrewRuntime(ctx, f.crew)
			if failure == "inspect-cancel" && !errors.Is(e, context.Canceled) {
				t.Fatalf("original cancellation lost: %v", e)
			}
			if e == nil {
				t.Fatal("failed preparation admitted")
			}
			if failure == "inspect" && !strings.Contains(e.Error(), "inspect injected") {
				t.Fatalf("lost inspect error: %v", e)
			}
			if f.starts != 0 || f.deletes != 1 || f.imageExecs != 0 {
				t.Fatalf("failed preparation stranded or started runtime: err=%v starts=%d removes=%d", e, f.starts, f.deletes)
			}
			if failure == "audit-remove" {
				if !strings.Contains(e.Error(), "remove injected") || !errors.Is(e, errStagedDenied) {
					t.Fatalf("cleanup error hid original error: %v", e)
				}
				if _, e := f.p.loadStagedEnv(f.c); e != nil {
					t.Fatalf("failed Docker removal discarded needed material: %v", e)
				}
				return
			}
			if failure == "audit" {
				if _, e := f.p.loadStagedEnv(f.c); e == nil {
					t.Fatal("failed create retained confidential material")
				}
			}
			// Clear the fault, allowing a second create to reach ContainerStart. Stop
			// there so this test cannot need a fake bootstrap or image workload.
			f.fail = ""
			f.beforeFailure = nil
			if f.faultDir != "" {
				if e := os.Chmod(f.faultDir, 0700); e != nil {
					t.Fatal(e)
				}
			}
			if e := os.Chmod(filepath.Join(f.p.cfg.OutputBasePath, runtimestage.DirName, "staged-env"), 0700); e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			f.p.stagedTestHook = func(phase, id string) error { return errors.New("retry reached keeper") }
			_, _ = f.p.EnsureCrewRuntime(t.Context(), f.crew)
			if f.runtimeCreates != 2 || f.starts != 1 {
				t.Fatalf("retry could not recover: creates=%d starts=%d", f.runtimeCreates, f.starts)
			}
		})
	}
}

func TestStagedLifecycleRemovalMaterial(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	for _, mode := range []string{"explicit", "image-recreate", "mount-recreate", "managed-mount-recreate", "backoff", "idle", "prune", "idle-veto", "idle-remove-fails", "prune-remove-fails", "remove-fails", "foreign", "file-symlink", "scope-symlink"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t, true)
			target, e := f.p.stagedEnvPath(f.c)
			if e != nil {
				t.Fatal(e)
			}
			outside := filepath.Join(t.TempDir(), filepath.Base(target))
			if e := os.WriteFile(outside, []byte("preserve"), 0600); e != nil {
				t.Fatal(e)
			}
			other := f.c
			other.Config = &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: f.p.cfg.InstanceID, crewCrewIDLabel: f.crew.ID, stagedEnvLabel: newStagedEnvHandle()}}
			other.ID = "other-runtime"
			if e := f.p.saveStagedEnv(other, []string{"OTHER=synthetic"}); e != nil {
				t.Fatal(e)
			}
			if mode == "file-symlink" {
				_ = os.Remove(target)
				if e := os.Symlink(outside, target); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "scope-symlink" {
				if e := os.Rename(filepath.Dir(target), filepath.Dir(target)+"-preserve"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Dir(outside), filepath.Dir(target)); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "foreign" {
				f.c.Config.Labels[resourcelifecycle.InstanceLabel] = "other-installation"
			}
			if mode == "remove-fails" || strings.HasSuffix(mode, "remove-fails") {
				f.fail = "remove"
			}
			switch mode {
			case "idle", "idle-veto", "idle-remove-fails":
				f.c.State = &container.State{Status: "exited"}
				veto := errors.New("active work veto")
				e := f.p.RemoveIdleRuntime(t.Context(), f.c.ID, f.crew.ID, func(c resourcelifecycle.RetentionContainer) error {
					if mode == "idle-veto" {
						return veto
					}
					return nil
				})
				if mode == "idle-veto" {
					if !errors.Is(e, veto) || f.deletes != 0 {
						t.Fatalf("idle-use veto lost: %v", e)
					}
				} else if f.removeForce || f.removeVolumes {
					t.Fatal("idle retention removal policy changed")
				}
			case "prune", "prune-remove-fails":
				_, _ = f.p.PruneCrewRuntimes(t.Context(), []provider.CrewRef{{ID: f.crew.ID, Slug: f.crew.Slug}})
				if !f.removeForce || f.removeVolumes {
					t.Fatal("crew prune removal policy changed")
				}
			case "image-recreate":
				f.c.State = &container.State{Status: "exited"}
				f.tagID = "sha256:" + strings.Repeat("b", 64)
				_, _ = f.p.EnsureCrewRuntime(t.Context(), f.crew)
			case "mount-recreate", "managed-mount-recreate":
				f.crew.CachedImage = lifecycleImage // Isolate cleanup from F2 tag drift on the baseline.
				kept := f.c.Mounts[:0]
				for _, m := range f.c.Mounts {
					if m.Destination != "/crew" {
						kept = append(kept, m)
					}
				}
				f.c.Mounts = kept
				ctx := t.Context()
				if mode == "managed-mount-recreate" {
					f.c.State = &container.State{Status: "exited"}
					ctx = context.WithValue(ctx, managedRuntimeUseKey{}, true)
				}
				_, _ = f.p.EnsureCrewRuntime(ctx, f.crew)
			case "backoff":
				f.c.State = &container.State{Status: "restarting", Restarting: true}
				_, _ = f.p.EnsureCrewRuntime(t.Context(), f.crew)
			default:
				_ = f.p.RemoveCrewRuntime(t.Context(), f.c.ID)
			}
			_, e = os.Lstat(target)
			retain := mode == "idle-veto" || strings.HasSuffix(mode, "remove-fails") || mode == "foreign" || mode == "file-symlink" || mode == "scope-symlink"
			if retain && e != nil {
				t.Fatalf("unowned/needed material removed: %v", e)
			}
			if !retain && !os.IsNotExist(e) {
				t.Fatalf("owned removed-runtime material retained: %v deletes=%d", e, f.deletes)
			}
			if raw, e := os.ReadFile(outside); e != nil || string(raw) != "preserve" {
				t.Fatalf("symlink outside modified: %v", e)
			}
			if _, e := f.p.loadStagedEnv(other); e != nil {
				t.Fatalf("other runtime material removed: %v", e)
			}
		})
	}
}
