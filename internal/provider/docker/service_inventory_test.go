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

// TestListCrewServices_FiltersByPrefixAndStripsName is the load-bearing
// case: given a mixed bag of containers (the crew's own runtime, another
// crew's sidecar, and this crew's two sidecars — one running, one
// stopped), only the matching-prefix containers come back, with the
// "<prefix>-svc-<slug>-" segment stripped so Name matches the manifest's
// service name again.
func TestListCrewServices_FiltersByPrefixAndStripsName(t *testing.T) {
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
					// The crew's own agent runtime — must NOT be picked up.
					"Id":    "team-cid",
					"Names": []string{"/crewship-team-alpha-crew1"},
					"Image": "crewship/agent:latest",
					"State": "running",
				},
				{
					// Another crew's sidecar — must NOT be picked up.
					"Id":    "other-cid",
					"Names": []string{"/crewship-svc-beta-redis"},
					"Image": "redis:7-alpine",
					"State": "running",
				},
				{
					"Id":     "redis-cid",
					"Names":  []string{"/crewship-svc-alpha-redis"},
					"Image":  "redis:7-alpine",
					"State":  "running",
					"Status": "Up 2 hours",
					"Ports":  []map[string]any{{"PrivatePort": 6379, "Type": "tcp"}},
				},
				{
					"Id":     "pg-cid",
					"Names":  []string{"/crewship-svc-alpha-postgres"},
					"Image":  "postgres:16",
					"State":  "exited",
					"Status": "Exited (0) 3 minutes ago",
					"Ports":  []map[string]any{{"PrivatePort": 5432, "Type": "tcp"}},
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
