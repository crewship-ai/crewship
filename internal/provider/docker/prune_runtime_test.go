package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// runtimeDaemonHandler mimics the Docker REST slice PruneCrewRuntimes touches:
// container list + remove, volume list + remove. The fixture holds, for crew
// (id "crew1", slug "engineering"): its id-scoped agent container + home/tools
// volumes, its sidecar container (matched by label) + sidecar volume, AND
// unrelated resources for a SECOND crew that must survive untouched.
func runtimeDaemonHandler(t *testing.T, prefix string, delC, delV *[]string) http.HandlerFunc {
	t.Helper()
	containers := []map[string]any{
		// Target crew: id-scoped agent container.
		{"Id": "agent1", "Names": []string{"/" + prefix + "-team-engineering-crew1"}},
		// Target crew: sidecar (no id-scoped name; matched by label).
		{"Id": "sidecar1", "Names": []string{"/" + prefix + "-svc-engineering-mcp"},
			"Labels": map[string]string{"crewship.crew": "engineering", "crewship.kind": "sidecar"}},
		// Other crew: must survive.
		{"Id": "agent2", "Names": []string{"/" + prefix + "-team-quality-crew2"}},
	}
	volumes := []map[string]any{
		{"Name": prefix + "-home-engineering-crew1"},   // target
		{"Name": prefix + "-tools-engineering-crew1"},  // target
		{"Name": prefix + "-svc-engineering-vol-work"}, // target sidecar vol
		{"Name": prefix + "-home-quality-crew2"},       // other crew, keep
	}
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode(containers)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": volumes, "Warnings": nil})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			parts := strings.Split(r.URL.Path, "/")
			*delC = append(*delC, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/volumes/"):
			parts := strings.Split(r.URL.Path, "/")
			*delV = append(*delV, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func TestPruneCrewRuntimes_RemovesIDScopedAndSidecarOnly(t *testing.T) {
	t.Parallel()

	var delC, delV []string
	p, cleanup := newFakeDockerProvider(t, runtimeDaemonHandler(t, "crewship", &delC, &delV))
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	removed, err := p.PruneCrewRuntimes(context.Background(),
		[]provider.CrewRef{{ID: "crew1", Slug: "engineering"}})
	if err != nil {
		t.Fatalf("PruneCrewRuntimes: %v", err)
	}

	// Containers: the id-scoped agent + the labelled sidecar, never crew2's.
	sort.Strings(delC)
	wantC := []string{"agent1", "sidecar1"}
	if strings.Join(delC, ",") != strings.Join(wantC, ",") {
		t.Errorf("container deletes = %v; want %v (other crew must survive)", delC, wantC)
	}
	// Volumes: home+tools+sidecar for the target crew, never crew2's home.
	sort.Strings(delV)
	wantV := []string{"crewship-home-engineering-crew1", "crewship-svc-engineering-vol-work", "crewship-tools-engineering-crew1"}
	if strings.Join(delV, ",") != strings.Join(wantV, ",") {
		t.Errorf("volume deletes = %v; want %v (other crew must survive)", delV, wantV)
	}
	// removed contains names (container names + volume names).
	for _, n := range removed {
		if strings.Contains(n, "quality") || strings.Contains(n, "crew2") {
			t.Errorf("removed a non-target resource %q", n)
		}
	}
	if len(removed) != 5 {
		t.Errorf("removed count = %d (%v); want 5", len(removed), removed)
	}
}

func TestPruneCrewRuntimes_NoCrewsNoop(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("empty crew set must not touch the daemon: %s %s", r.Method, r.URL.Path)
	})
	defer cleanup()

	removed, err := p.PruneCrewRuntimes(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v; want none", removed)
	}
}

// A ref missing id or slug can't form an unambiguous id-scoped name; it must be
// skipped rather than risk matching a legacy (slug-only) resource. With only
// such a ref, the daemon must never be listed.
func TestPruneCrewRuntimes_IncompleteRefSkipped(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("incomplete ref must not touch the daemon: %s %s", r.Method, r.URL.Path)
	})
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	removed, err := p.PruneCrewRuntimes(context.Background(),
		[]provider.CrewRef{{Slug: "engineering"}}) // no ID
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v; want none", removed)
	}
}

// Provider must satisfy the optional interface so the API layer can
// type-assert it.
var _ provider.CrewRuntimePruner = (*Provider)(nil)
