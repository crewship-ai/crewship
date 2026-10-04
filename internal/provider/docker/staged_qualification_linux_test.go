//go:build linux

package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/moby/moby/api/types/container"
)

func TestStagedQualificationArtifactRevision(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	for _, artifact := range []string{"keeper", "bootstrap"} {
		for _, state := range []string{"running", "exited"} {
			t.Run(artifact+"/"+state, func(t *testing.T) {
				f := newQualifiedFixture(t, true)
				if state == "exited" {
					f.c.State = &container.State{Status: "exited"}
				}

				source := f.p.cfg.EntrypointPath
				if artifact == "keeper" {
					source = f.p.cfg.SidecarBinaryPath
				}
				raw, e := os.ReadFile(source)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(source); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(source, append(raw, 0), 0555); e != nil {
					t.Fatal(e)
				}
				removed, e := f.p.reconcileStagedRevision(t.Context(), f.crew, f.c)
				if state == "exited" {
					if e != nil || !removed || f.deletes != 1 || f.runtimeCreates != 0 || f.removeForce || f.removeVolumes {
						t.Fatalf("stopped revision not safely replaced: err=%v removes=%d creates=%d force=%v volumes=%v", e, f.deletes, f.runtimeCreates, f.removeForce, f.removeVolumes)
					}
				} else if !errors.Is(e, provider.ErrRuntimeImageUpdatePending) || f.deletes != 0 || f.starts != 0 {
					t.Fatalf("running revision interrupted or bricked without actionable pending update: err=%v removes=%d starts=%d", e, f.deletes, f.starts)
				}
			})
		}
	}
}

func TestStagedQualificationReadyReuseDoesNotCreateFenceHelpers(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	f := newQualifiedFixture(t, true)
	for i := 0; i < 3; i++ {
		if e := f.p.ensureStagedStart(t.Context(), f.crew, f.c.ID); e != nil {
			t.Fatal(e)
		}
		if f.helperCreates != 2 {
			t.Fatalf("unchanged ready reuse created helper containers: attempt=%d helpers=%d", i, f.helperCreates)
		}
	}
	f.c.State.StartedAt = "2026-10-04T00:01:00Z"
	if e := f.p.ensureStagedStart(t.Context(), f.crew, f.c.ID); e != nil {
		t.Fatal(e)
	}
	if f.helperCreates != 4 {
		t.Fatalf("new boot omitted apply/check: helpers=%d", f.helperCreates)
	}
}

func TestStagedQualificationRevisionCannotMaskUnsafeRuntime(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	for _, unsafe := range []string{"config", "keeper-bind", "bootstrap-bind", "ownership", "material", "alias-parent"} {
		t.Run(unsafe, func(t *testing.T) {
			f := newQualifiedFixture(t, true)
			raw, e := os.ReadFile(f.p.cfg.SidecarBinaryPath)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(f.p.cfg.SidecarBinaryPath); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(f.p.cfg.SidecarBinaryPath, append(raw, 0), 0555); e != nil {
				t.Fatal(e)
			}
			switch unsafe {
			case "config":
				f.c.HostConfig.Privileged = true
			case "alias-parent":
				f.c.Mounts = append(f.c.Mounts, container.MountPoint{Type: "volume", Source: "untrusted-feature", Destination: "/usr//local/bin"})
			case "ownership":
				f.c.Config.Labels[resourcelifecycle.InstanceLabel] = "foreign"
			case "material":
				target, e := f.p.stagedEnvPath(f.c)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(target); e != nil {
					t.Fatal(e)
				}
			default:
				target := stagedstart.Binary
				if unsafe == "bootstrap-bind" {
					target = stagedstart.Bootstrap
				}
				for i := range f.c.Mounts {
					if f.c.Mounts[i].Destination == target {
						f.c.Mounts[i].RW = true
					}
				}
			}
			_, e = f.p.reconcileStagedRevision(t.Context(), f.crew, f.c)
			if e == nil || errors.Is(e, provider.ErrRuntimeImageUpdatePending) || f.deletes != 0 || f.stops != 0 {
				t.Fatalf("revision masked unsafe actual runtime: err=%v removes=%d stops=%d", e, f.deletes, f.stops)
			}
		})
	}
}

func TestStagedQualificationMissingStartCannotReuseReadback(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	f := newQualifiedFixture(t, true)
	if e := f.p.ensureStagedStart(t.Context(), f.crew, f.c.ID); e != nil {
		t.Fatal(e)
	}
	f.c.State.StartedAt = ""
	if e := f.p.ensureStagedStart(t.Context(), f.crew, f.c.ID); !errors.Is(e, errStagedDenied) {
		t.Fatalf("missing start timestamp reused cached readback: %v", e)
	}
	if f.helperCreates != 2 {
		t.Fatalf("invalid generation launched a helper: %d", f.helperCreates)
	}
}
func TestStagedExecNonPilotDoesNotAddInspect(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprint(interactive), func(t *testing.T) {
			var inspections atomic.Int32
			p := newCovProviderTCP(t, Config{EgressFenceCrews: []string{"selected-other-crew"}}, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/cid/json"):
					inspections.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid", "Config": map[string]any{"Labels": map[string]string{crewCrewIDLabel: "nonpilot"}}})
				case strings.HasSuffix(r.URL.Path, "/containers/cid/exec"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"Id":"e1"}`))
				case strings.Contains(r.URL.Path, "/exec/e1/start"):
					covHijackUpgrade(t, w, r, covStdcopyFrame(1, "ok"))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			})
			if interactive {
				result, e := p.ExecInteractive(t.Context(), provider.InteractiveExecConfig{ContainerID: "cid", Cmd: []string{"echo", "hi"}, User: "1001:1001"})
				if e != nil {
					t.Fatal(e)
				}
				result.Conn.Close()
			} else {
				result, e := p.Exec(t.Context(), provider.ExecConfig{ContainerID: "cid", Cmd: []string{"echo", "hi"}, User: "1001:1001"})
				if e != nil {
					t.Fatal(e)
				}
				result.Reader.Close()
			}
			if inspections.Load() != 1 {
				t.Fatalf("nonpilot gained a staged inspect: %d", inspections.Load())
			}
		})
	}
}

func TestStagedQualificationTagIdentity(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	f := newQualifiedFixture(t, true)
	if drift, e := f.p.stagedImageDrift(t.Context(), f.c, f.crew.Image); e != nil || drift {
		t.Fatalf("unchanged tag: %v %v", drift, e)
	}
	f.tagID = "sha256:" + strings.Repeat("b", 64)
	if drift, e := f.p.stagedImageDrift(t.Context(), f.c, f.crew.Image); e != nil || !drift {
		t.Fatalf("replaced tag: %v %v", drift, e)
	}
	f.tagID = ""
	if _, e := f.p.stagedImageDrift(t.Context(), f.c, f.crew.Image); !errors.Is(e, errStagedDenied) {
		t.Fatal(e)
	}
}

func TestStagedQualificationPreparationRollback(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	f := newQualifiedFixture(t, true)
	f.c.State = &container.State{Status: "created"}
	f.c.HostConfig.Privileged = true
	if e := f.p.prepareCreatedStaged(t.Context(), f.c.ID, f.c.Config, []string{"TOKEN=synthetic"}, f.crew); !errors.Is(e, errStagedDenied) {
		t.Fatal(e)
	}
	if f.exists || f.deletes != 1 || !f.removeForce || !f.removeVolumes || f.starts != 0 {
		t.Fatalf("rollback widened or failed: %+v", f)
	}
	if _, e := f.p.loadStagedEnv(f.c); e == nil {
		t.Fatal("removed runtime retained private material")
	}
}

func TestStagedQualificationExternalExecRequiresGate(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	f := newQualifiedFixture(t, true)
	if e := f.p.ensureStagedStart(t.Context(), f.crew, f.c.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.p.GuardExternalExec(t.Context(), f.c.ID); !errors.Is(e, errStagedDenied) {
		t.Fatalf("raw external workload admission: %v", e)
	}
}
