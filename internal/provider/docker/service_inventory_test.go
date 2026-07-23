package docker

// Tests for ListCrewServices — the live-Docker-read crew service
// inventory (GET /api/v1/crews/{crewId}/services). Uses the fake HTTP
// API harness (fakeapi_test.go) so it runs on a pure Go test, no live
// daemon required.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestListCrewServices_FiltersByLabelAndReadsName is the load-bearing
// case: given a mixed bag of containers (the crew's own runtime, another
// crew's sidecar, and this crew's two sidecars — one running, one
// stopped), only the containers carrying this crew's crewship.crew +
// crewship.kind=sidecar labels come back, and Name is read from the
// authoritative crewship.svc label (== the manifest's service name).
func TestListCrewServices_FiltersByLabelAndReadsName(t *testing.T) {
	t.Parallel()

	// ListCrewServices does a ContainerList (name-match) followed by a
	// ContainerStatus inspect per match, so the fake server has to
	// answer both /containers/json AND /containers/{id}/json.
	inspectState := map[string]map[string]any{
		"redis-cid": {"Running": true, "StartedAt": "2026-01-01T00:00:00Z"},
		"pg-cid":    {"StartedAt": "2026-01-01T00:00:00Z"}, // no Running flag => stopped
	}
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					// The crew's own agent runtime — must NOT be picked
					// up (it is not a sidecar: no crewship.kind label).
					"Id":     "team-cid",
					"Names":  []string{"/crewship-team-alpha-crew1"},
					"Image":  "crewship/agent:latest",
					"State":  "running",
					"Labels": map[string]any{"crewship.crew": "alpha"},
				},
				{
					// Another crew's sidecar — must NOT be picked up
					// (crewship.crew label is "beta", not "alpha").
					"Id":     "other-cid",
					"Names":  []string{"/crewship-svc-beta-redis"},
					"Image":  "redis:7-alpine",
					"State":  "running",
					"Labels": map[string]any{"crewship.crew": "beta", "crewship.kind": "sidecar", "crewship.svc": "redis"},
				},
				{
					"Id":     "redis-cid",
					"Names":  []string{"/crewship-svc-alpha-redis"},
					"Image":  "redis:7-alpine",
					"State":  "running",
					"Status": "Up 2 hours",
					"Ports":  []map[string]any{{"PrivatePort": 6379, "Type": "tcp"}},
					"Labels": map[string]any{"crewship.crew": "alpha", "crewship.kind": "sidecar", "crewship.svc": "redis"},
				},
				{
					"Id":     "pg-cid",
					"Names":  []string{"/crewship-svc-alpha-postgres"},
					"Image":  "postgres:16",
					"State":  "exited",
					"Status": "Exited (0) 3 minutes ago",
					"Ports":  []map[string]any{{"PrivatePort": 5432, "Type": "tcp"}},
					"Labels": map[string]any{"crewship.crew": "alpha", "crewship.kind": "sidecar", "crewship.svc": "postgres"},
				},
			})
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/containers/"):], "/containers/"), "/json")
			state, ok := inspectState[id]
			if !ok {
				t.Errorf("unexpected inspect for container id %q", id)
				http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": state})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	got, err := p.ListCrewServices(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ListCrewServices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 services for crew alpha, got %d: %+v", len(got), got)
	}

	byName := map[string]provider.CrewServiceStatus{}
	for _, s := range got {
		byName[s.Name] = s
	}

	redis, ok := byName["redis"]
	if !ok {
		t.Fatalf("missing redis service in %+v", got)
	}
	if redis.Image != "redis:7-alpine" {
		t.Errorf("redis image = %q", redis.Image)
	}
	if redis.State != "running" {
		t.Errorf("redis state = %q, want running", redis.State)
	}
	if len(redis.Ports) != 1 || redis.Ports[0] != "6379/tcp" {
		t.Errorf("redis ports = %+v, want [6379/tcp]", redis.Ports)
	}

	pg, ok := byName["postgres"]
	if !ok {
		t.Fatalf("missing postgres service in %+v", got)
	}
	if pg.State != "stopped" {
		t.Errorf("postgres state = %q, want stopped (live, not stale)", pg.State)
	}
}

// TestListCrewServices_NoCrossCrewPrefixLeak is the adversarial case:
// two DISTINCT crews whose slugs share a hyphen boundary — "alpha" and
// "alpha-foo". Crew slugs are DNS-label-shaped and MAY contain hyphens
// (validSlugRe = ^[a-z0-9][a-z0-9_-]*$), so "alpha-foo" is a perfectly
// legal, unrelated crew. Its sidecar container is named
// "crewship-svc-alpha-foo-redis", which — fatally for a naive
// strings.HasPrefix("crewship-svc-alpha-") matcher — STARTS WITH crew
// "alpha"'s prefix. A prefix-only matcher therefore reports crew
// alpha-foo's redis (image version, ports, live state) as if it were
// crew alpha's service "foo-redis": a cross-crew (and, since slugs are
// only workspace-unique while the docker daemon is shared instance-wide,
// cross-TENANT) information disclosure. This test asserts crew "alpha"
// sees ONLY its own sidecar. It fails on the prefix matcher and passes
// once ListCrewServices keys off the exact crewship.crew label (the same
// authoritative match Stop/RemoveCrewServices already use).
func TestListCrewServices_NoCrossCrewPrefixLeak(t *testing.T) {
	t.Parallel()

	inspectState := map[string]map[string]any{
		"alpha-redis-cid": {"Running": true, "StartedAt": "2026-01-01T00:00:00Z"},
	}
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					// Crew "alpha"'s own sidecar — the ONLY row that
					// must come back.
					"Id":     "alpha-redis-cid",
					"Names":  []string{"/crewship-svc-alpha-redis"},
					"Image":  "redis:7-alpine",
					"State":  "running",
					"Status": "Up 1 hour",
					"Labels": map[string]any{
						"crewship.crew": "alpha",
						"crewship.kind": "sidecar",
						"crewship.svc":  "redis",
					},
				},
				{
					// Crew "alpha-foo"'s sidecar. Its name shares crew
					// alpha's "crewship-svc-alpha-" prefix but it belongs
					// to a DIFFERENT crew — must NOT leak into alpha.
					"Id":     "alphafoo-redis-cid",
					"Names":  []string{"/crewship-svc-alpha-foo-redis"},
					"Image":  "redis:5.0.0", // an old, CVE-laden version — recon value
					"State":  "running",
					"Status": "Up 5 days",
					"Ports":  []map[string]any{{"PrivatePort": 6379, "Type": "tcp"}},
					"Labels": map[string]any{
						"crewship.crew": "alpha-foo",
						"crewship.kind": "sidecar",
						"crewship.svc":  "redis",
					},
				},
			})
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/containers/"):], "/containers/"), "/json")
			state, ok := inspectState[id]
			if !ok {
				t.Errorf("unexpected inspect for container id %q (should not inspect another crew's container)", id)
				http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": state})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	got, err := p.ListCrewServices(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ListCrewServices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("cross-crew LEAK: crew alpha should see exactly 1 service (its own redis), got %d: %+v", len(got), got)
	}
	if got[0].Name != "redis" {
		t.Errorf("service name = %q, want %q (a name like %q proves alpha-foo's container leaked in)", got[0].Name, "redis", "foo-redis")
	}
	if got[0].Image == "redis:5.0.0" {
		t.Errorf("cross-crew LEAK: crew alpha-foo's image %q surfaced in crew alpha's inventory", got[0].Image)
	}
}

// TestListCrewServices_EmptySlug_Errors guards against a caller
// accidentally scoping the daemon-wide list by an empty prefix, which
// would match everything.
func TestListCrewServices_EmptySlug_Errors(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach the daemon with an empty slug")
	})
	defer cleanup()

	if _, err := p.ListCrewServices(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty crew slug")
	}
}

// TestListCrewServices_NoMatches_EmptySlice confirms a crew with no
// sidecars gets an empty (non-nil-shaped-in-JSON) slice, not an error.
func TestListCrewServices_NoMatches_EmptySlice(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	got, err := p.ListCrewServices(context.Background(), "lonely")
	if err != nil {
		t.Fatalf("ListCrewServices: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 services, got %d", len(got))
	}
}

// TestListCrewServices_ListError_Wraps confirms a daemon-list failure
// propagates as a wrapped error rather than a silently empty result.
func TestListCrewServices_ListError_Wraps(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"daemon down"}`, http.StatusInternalServerError)
	})
	defer cleanup()

	_, err := p.ListCrewServices(context.Background(), "alpha")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "list containers") {
		t.Errorf("error should mention 'list containers': %v", err)
	}
}
