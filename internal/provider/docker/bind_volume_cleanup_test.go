package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/volume"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func bindVolumeFixture(name string) volume.Volume {
	return volume.Volume{Name: name, Driver: "local", Labels: map[string]string{
		resourcelifecycle.InstanceLabel: "installation-a", crewCrewIDLabel: "crew", crewKindLabel: noexecBindVolumeKind,
	}, Options: map[string]string{"type": "none", "device": "/persistent/crew", "o": noexecBindMountOpts}}
}

func TestBindVolumeReaperOwnershipFakeAPI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*volume.Volume)
		eligible bool
	}{
		{"owned", func(*volume.Volume) {}, true},
		{"foreign", func(v *volume.Volume) { v.Labels[resourcelifecycle.InstanceLabel] = "installation-b" }, false},
		{"legacy", func(v *volume.Volume) { v.Labels = nil }, false},
		{"missing-crew", func(v *volume.Volume) { delete(v.Labels, crewCrewIDLabel) }, false},
		{"home", func(v *volume.Volume) { v.Name = "crewship-home-slug-crew" }, false},
		{"tools", func(v *volume.Volume) { v.Name = "crewship-tools-slug-crew" }, false},
		{"service", func(v *volume.Volume) { v.Labels[crewKindLabel] = "sidecar" }, false},
		{"managed-storage", func(v *volume.Volume) { v.Options = nil }, false},
		{"other-driver", func(v *volume.Volume) { v.Driver = "nfs" }, false},
		{"exec-bind", func(v *volume.Volume) { v.Options["o"] = "bind" }, false},
		{"relative-device", func(v *volume.Volume) { v.Options["device"] = "relative" }, false},
		{"nonhex-name", func(v *volume.Volume) { v.Name = strings.Repeat("z", 64) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := bindVolumeFixture(strings.Repeat("a", 64))
			tc.mutate(&v)
			removed := 0
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/volumes"):
					var filters map[string]map[string]bool
					if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); err != nil {
						t.Error(err)
					}
					if !filters["dangling"]["true"] || !filters["label"][resourcelifecycle.InstanceLabel+"=installation-a"] || !filters["label"][crewKindLabel+"="+noexecBindVolumeKind] {
						t.Errorf("unsafe filters %v", filters)
					}
					// Deliberately ignore filters to prove the client rechecks them.
					json.NewEncoder(w).Encode(map[string]any{"Volumes": []volume.Volume{v}})
				case r.Method == http.MethodGet:
					if !tc.eligible {
						t.Error("inspected ineligible volume")
					}
					json.NewEncoder(w).Encode(v)
				case r.Method == http.MethodDelete:
					if !tc.eligible || strings.TrimPrefix(r.URL.Path, "/v1.43/volumes/") != v.Name {
						t.Error("removed ineligible volume")
					}
					if r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true" {
						t.Error("forced removal")
					}
					removed++
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			})
			defer close()
			if err := (&cleanupRuntime{client: p.client}).ReapBindVolumes(context.Background(), "installation-a", 100); err != nil {
				t.Fatal(err)
			}
			if (removed == 1) != tc.eligible {
				t.Fatalf("removed=%d eligible=%v", removed, tc.eligible)
			}
		})
	}
}

func TestBindVolumeReaperFailuresAndRacesFakeAPI(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		code        int
		wantErr     bool
	}{
		{"list-failed", "list", 500, true}, {"incomplete-list", "warning", 200, true},
		{"inspect-failed", "inspect", 500, true}, {"already-gone-inspect", "inspect", 404, false},
		{"ownership-changed", "foreign", 200, false}, {"name-changed", "name", 200, false},
		{"remove-failed", "remove", 500, true}, {"permission-denied", "remove", 403, true},
		{"in-use-stopped-or-racing", "remove", 409, false}, {"already-gone-remove", "remove", 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := bindVolumeFixture(strings.Repeat("a", 64))
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/volumes") {
					if tc.stage == "list" {
						http.Error(w, `{"message":"failed"}`, tc.code)
						return
					}
					warnings := []string{}
					if tc.stage == "warning" {
						warnings = append(warnings, "failed driver")
					}
					json.NewEncoder(w).Encode(map[string]any{"Volumes": []volume.Volume{v}, "Warnings": warnings})
					return
				}
				if r.Method == http.MethodGet {
					if tc.stage == "inspect" {
						http.Error(w, `{"message":"failed"}`, tc.code)
						return
					}
					fresh := bindVolumeFixture(v.Name)
					if tc.stage == "foreign" {
						fresh.Labels[resourcelifecycle.InstanceLabel] = "foreign"
					}
					if tc.stage == "name" {
						fresh.Name = strings.Repeat("b", 64)
					}
					json.NewEncoder(w).Encode(fresh)
					return
				}
				if tc.stage != "remove" {
					t.Errorf("unexpected removal for %s", tc.stage)
				}
				http.Error(w, `{"message":"failed"}`, tc.code)
			})
			defer close()
			if err := (&cleanupRuntime{client: p.client}).ReapBindVolumes(context.Background(), "installation-a", 100); (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestBindVolumeReaperDisabledAndBatchFakeAPI(t *testing.T) {
	removed := 0
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		v := bindVolumeFixture(strings.Repeat("a", 64))
		switch {
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			json.NewEncoder(w).Encode(map[string]any{"Volumes": []volume.Volume{v, bindVolumeFixture(strings.Repeat("b", 64))}})
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(v)
		case r.Method == http.MethodDelete:
			removed++
			w.WriteHeader(http.StatusNoContent)
		}
	})
	defer close()
	rt := &cleanupRuntime{client: p.client}
	if err := rt.ReapBindVolumes(context.Background(), "", 100); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReapBindVolumes(context.Background(), "installation-a", 0); err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatal("disabled reaper made mutations")
	}
	if err := rt.ReapBindVolumes(context.Background(), "installation-a", 1); err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d, want batch of 1", removed)
	}
}

func TestCrewMountsLabelOnlyBindRecords(t *testing.T) {
	for _, instance := range []string{"installation-a", ""} {
		p := &Provider{cfg: Config{InstanceID: instance, SidecarBinaryPath: "/sidecar", EntrypointPath: "/entrypoint"}}
		mounts, err := p.buildMounts("crew", "slug", "/ws", "/out", "/crew")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mounts {
			if !agentWritableNoexecTargets[m.Target] {
				if m.VolumeOptions != nil && m.VolumeOptions.Labels[crewKindLabel] == noexecBindVolumeKind {
					t.Fatalf("history mount labelled ephemeral: %+v", m)
				}
				continue
			}
			if m.Source != "" || m.VolumeOptions == nil || !m.VolumeOptions.NoCopy {
				t.Fatalf("wrong bind mount %+v", m)
			}
			labels := m.VolumeOptions.Labels
			if labels[resourcelifecycle.InstanceLabel] != instance || labels[crewCrewIDLabel] != "crew" || labels[crewKindLabel] != noexecBindVolumeKind {
				t.Fatalf("labels %+v", labels)
			}
		}
	}
}

func TestReconcileTeardownRemovalFailureFakeAPI(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					http.Error(w, `{"message":"stop failed"}`, 500)
					return
				}
				if r.Method != http.MethodDelete || (r.URL.Query().Get("v") != "1" && r.URL.Query().Get("v") != "true") {
					t.Errorf("unsafe teardown %s %s", r.Method, r.URL)
				}
				w.WriteHeader(status)
			})
			defer close()
			p.setWarm("crew", "container", "image")
			err := p.removeForReconcile(context.Background(), "container", "crew")
			if (err != nil) != (status == 500) {
				t.Fatalf("error=%v status=%d", err, status)
			}
			if _, ok := p.warmHit("crew", "image"); ok {
				t.Fatal("teardown left stale runtime readiness cached")
			}
		})
	}
}

func TestRestartBackoffTeardownFailureStopsReconciliation(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Write([]byte(`[{"Id":"runtime","Names":["/crew-runtime"]}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/runtime/json"):
			w.Write([]byte(`{"Id":"runtime","Config":{"Image":"image"},"State":{"Status":"restarting","Restarting":true}}`))
		case strings.HasSuffix(r.URL.Path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			http.Error(w, `{"message":"remove failed"}`, http.StatusInternalServerError)
		default:
			t.Errorf("unexpected reconciliation %s %s", r.Method, r.URL.Path)
		}
	})
	defer close()
	_, done, err := p.reconcileExistingContainer(context.Background(), provider.CrewConfig{ID: "crew"}, "crew-runtime", "image", false, func(devcontainer.ProvisionEvent) {})
	if err == nil || done {
		t.Fatalf("failed teardown fell through to create: done=%v error=%v", done, err)
	}
}
