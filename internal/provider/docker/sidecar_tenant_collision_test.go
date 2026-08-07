package docker

// Audit C1, applied to sidecars (#1732).
//
// CrewContainerName has folded the globally-unique crew id into the crew's
// container and home/tools volume names since C1, because `crews` is
// UNIQUE(workspace_id, slug) — a slug identifies a crew only WITHIN a
// workspace, while one crewshipd serves every workspace against one daemon.
//
// Sidecars (the Redis / Postgres / MySQL containers a crew declares in
// services_json) never got that treatment: their container name, their volume
// names and their `crewship.crew` label all carried the slug alone. Two
// workspaces that each named a crew `data-crew` therefore resolved to ONE
// sidecar container and ONE data volume — the second crew's start silently
// reattached to the first's, so two tenants shared one database. Deleting
// either crew force-removed the other's live container and deleted its volume.
//
// These tests drive the real EnsureCrewServices / RemoveCrewServices /
// RemoveCrewServiceVolumes code paths against a stateful fake daemon and
// assert the invariant directly: same slug + different workspace ⇒ different
// container, different volume, and a teardown that cannot reach the other.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Two workspaces, each with a crew slugged "data-crew" — legal (and common:
// every dev slot seeds the same names) because slugs are workspace-unique.
const (
	sidecarCollidingSlug = "data-crew"
	tenantACrewID        = "ckaaaadatacrew01"
	tenantBCrewID        = "ckbbbbdatacrew02"
)

// fakeSidecarDaemon is a stateful stand-in for the slice of the Docker REST
// API the sidecar lifecycle touches. Unlike the one-shot handlers in
// sidecar_cov_test.go it REMEMBERS what was created, which is the whole point
// here: the collision only shows up when the second crew's ensure observes the
// first crew's container and volume.
type fakeSidecarDaemon struct {
	mu sync.Mutex

	nextID     int
	waitExit   int64 // exit code the copy helper reports
	volumeErr  bool  // VolumeList answers 500
	containers []fakeSidecarContainer
	volumes    []fakeSidecarVolume

	// refcountVolumes makes DELETE /volumes/{name} answer 409 "volume is in
	// use" while some container still mounts it — which is what every real
	// daemon does, force flag or not. Opt-in so the tests that exercise the
	// teardown in isolation (they remove volumes without first removing the
	// containers that mount them, deliberately) keep their current shape.
	refcountVolumes bool

	// recorded, in call order
	createdNames   []string
	createdVolumes []string
	removedIDs     []string
	removedVolumes []string
	mountSources   []string
}

type fakeSidecarContainer struct {
	ID     string
	Name   string
	Image  string
	State  string
	Labels map[string]string
	Mounts []string // volume names this container references
}

type fakeSidecarVolume struct {
	Name   string
	Labels map[string]string
}

func (f *fakeSidecarDaemon) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
			out := make([]map[string]any, 0, len(f.containers))
			for _, c := range f.containers {
				out = append(out, map[string]any{
					"Id":     c.ID,
					"Names":  []string{"/" + c.Name},
					"Image":  c.Image,
					"State":  c.State,
					"Labels": c.Labels,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/volumes"):
			if f.volumeErr {
				http.Error(w, `{"message":"volume backend down"}`, http.StatusInternalServerError)
				return
			}
			out := make([]map[string]any, 0, len(f.volumes))
			for _, v := range f.volumes {
				out = append(out, map[string]any{"Name": v.Name, "Labels": v.Labels})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": out})

		case r.Method == http.MethodGet && strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))

		case r.Method == http.MethodGet && strings.Contains(path, "/volumes/"):
			name := path[strings.LastIndex(path, "/volumes/")+len("/volumes/"):]
			for _, v := range f.volumes {
				if v.Name == name {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Name": v.Name, "Labels": v.Labels,
						"Mountpoint": "/var/lib/docker/volumes/" + v.Name + "/_data",
					})
					return
				}
			}
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/volumes/create"):
			var vreq struct {
				Name   string
				Labels map[string]string
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &vreq)
			f.createdVolumes = append(f.createdVolumes, vreq.Name)
			f.volumes = append(f.volumes, fakeSidecarVolume{Name: vreq.Name, Labels: vreq.Labels})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": vreq.Name, "Labels": vreq.Labels})

		case r.Method == http.MethodDelete && strings.Contains(path, "/volumes/"):
			name := path[strings.LastIndex(path, "/volumes/")+len("/volumes/"):]
			if f.refcountVolumes {
				for _, c := range f.containers {
					for _, m := range c.Mounts {
						if m == name {
							http.Error(w, fmt.Sprintf(`{"message":"remove %s: volume is in use - [%s]"}`, name, c.ID),
								http.StatusConflict)
							return
						}
					}
				}
			}
			f.removedVolumes = append(f.removedVolumes, name)
			kept := f.volumes[:0]
			for _, v := range f.volumes {
				if v.Name != name {
					kept = append(kept, v)
				}
			}
			f.volumes = kept
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/containers/create"):
			var req struct {
				Image      string
				Labels     map[string]string
				HostConfig struct {
					Mounts []struct{ Source string }
				}
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			name := r.URL.Query().Get("name")
			f.nextID++
			id := fmt.Sprintf("cid-%d", f.nextID)
			f.createdNames = append(f.createdNames, name)
			mounts := make([]string, 0, len(req.HostConfig.Mounts))
			for _, m := range req.HostConfig.Mounts {
				f.mountSources = append(f.mountSources, m.Source)
				mounts = append(mounts, m.Source)
			}
			f.containers = append(f.containers, fakeSidecarContainer{
				ID: id, Name: name, Image: req.Image, State: "created", Labels: req.Labels, Mounts: mounts,
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			id := containerIDFromPath(path)
			for i := range f.containers {
				if f.containers[i].ID == id {
					f.containers[i].State = "running"
				}
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(path, "/wait"):
			// The legacy-volume migration's copy helper.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": f.waitExit})

		case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
			id := containerIDFromPath(path + "/x")
			f.removedIDs = append(f.removedIDs, id)
			kept := f.containers[:0]
			for _, c := range f.containers {
				if c.ID != id {
					kept = append(kept, c)
				}
			}
			f.containers = kept
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

// containerIDFromPath pulls the id out of "/v1.43/containers/<id>/<verb>".
func containerIDFromPath(path string) string {
	rest := path[strings.LastIndex(path, "/containers/")+len("/containers/"):]
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

func (f *fakeSidecarDaemon) snapshot() ([]string, []string, []string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.containers))
	for _, c := range f.containers {
		names = append(names, c.Name)
	}
	vols := make([]string, 0, len(f.volumes))
	for _, v := range f.volumes {
		vols = append(vols, v.Name)
	}
	return names, vols, append([]string(nil), f.createdNames...), append([]string(nil), f.createdVolumes...)
}

func collidingPostgres() provider.CrewService {
	return provider.CrewService{
		Name:    "postgres",
		Image:   "postgres:16",
		Volumes: []provider.CrewServiceVolume{{Name: "pgdata", Mount: "/var/lib/postgresql/data"}},
	}
}

// TestSidecarTenantCollision_TwoWorkspacesGetSeparateResources is the #1732
// reproducer. Two crews with the identical slug in different workspaces each
// start a `postgres` sidecar against ONE daemon. Before the fix the second
// ensure found the first crew's container by name, saw a matching image and
// spec hash, and returned its id — one container and one volume between two
// tenants.
func TestSidecarTenantCollision_TwoWorkspacesGetSeparateResources(t *testing.T) {
	t.Parallel()

	d := &fakeSidecarDaemon{}
	p := newCovProvider(t, Config{}, d.handler(t))

	svc := collidingPostgres()
	idsA, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantACrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{svc},
	})
	if err != nil {
		t.Fatalf("tenant A EnsureCrewServices: %v", err)
	}
	idsB, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{
		ID: tenantBCrewID, Slug: sidecarCollidingSlug,
		Services: []provider.CrewService{svc},
	})
	if err != nil {
		t.Fatalf("tenant B EnsureCrewServices: %v", err)
	}

	if idsA["postgres"] == idsB["postgres"] {
		t.Fatalf("C1/#1732 REGRESSION: two workspaces with crew slug %q share ONE sidecar container (%s) — "+
			"two tenants, one database", sidecarCollidingSlug, idsA["postgres"])
	}

	names, vols, created, createdVols := d.snapshot()
	if len(created) != 2 {
		t.Fatalf("C1/#1732 REGRESSION: %d sidecar container(s) created for two crews, want 2: %v", len(created), created)
	}
	if created[0] == created[1] {
		t.Fatalf("C1/#1732 REGRESSION: both crews created the container name %q", created[0])
	}
	if len(createdVols) != 2 {
		t.Fatalf("C1/#1732 REGRESSION: %d sidecar volume(s) created for two crews, want 2: %v — "+
			"a shared volume is a shared data directory", len(createdVols), createdVols)
	}

	// Each resource must carry its own crew id, the way CrewContainerName does.
	for _, want := range []struct {
		crewID string
		set    []string
	}{{tenantACrewID, names}, {tenantBCrewID, names}, {tenantACrewID, vols}, {tenantBCrewID, vols}} {
		found := false
		for _, n := range want.set {
			if strings.Contains(n, want.crewID) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no resource in %v carries crew id %q — names must be namespaced by the globally-unique crew id", want.set, want.crewID)
		}
	}

	// The mounts the containers were created with must be distinct volumes.
	d.mu.Lock()
	mounts := append([]string(nil), d.mountSources...)
	d.mu.Unlock()
	if len(mounts) == 2 && mounts[0] == mounts[1] {
		t.Fatalf("C1/#1732 REGRESSION: both crews' postgres mounted the same volume %q", mounts[0])
	}
}
