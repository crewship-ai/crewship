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

// legacyPruneHandler mimics the tiny slice of the Docker REST API the prune
// path touches: container list + remove, volume list + remove. It records the
// names targeted for deletion so tests can assert exactly which resources were
// pruned. The fixture daemon holds, for slug "engineering": the three legacy
// slug-only resources AND their id-scoped (post-C1) siblings, which must be
// left untouched.
func legacyPruneHandler(t *testing.T, prefix string, deletedContainers, deletedVolumes *[]string) http.HandlerFunc {
	t.Helper()
	// id "legacyteam" → legacy container; "idteam" → id-scoped container.
	containers := []map[string]any{
		{"Id": "legacyteam", "Names": []string{"/" + prefix + "-team-engineering"}},
		{"Id": "idteam", "Names": []string{"/" + prefix + "-team-engineering-crew1"}},
	}
	volumes := []map[string]any{
		{"Name": prefix + "-home-engineering"},        // legacy
		{"Name": prefix + "-tools-engineering"},       // legacy
		{"Name": prefix + "-home-engineering-crew1"},  // id-scoped, keep
		{"Name": prefix + "-tools-engineering-crew1"}, // id-scoped, keep
	}
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode(containers)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": volumes, "Warnings": nil})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			parts := strings.Split(r.URL.Path, "/")
			*deletedContainers = append(*deletedContainers, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/volumes/"):
			parts := strings.Split(r.URL.Path, "/")
			*deletedVolumes = append(*deletedVolumes, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func TestPruneLegacyCrewResources_RemovesOnlyLegacy(t *testing.T) {
	t.Parallel()

	var delC, delV []string
	p, cleanup := newFakeDockerProvider(t, legacyPruneHandler(t, "crewship", &delC, &delV))
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	removed, err := p.PruneLegacyCrewResources(context.Background(), []string{"engineering"})
	if err != nil {
		t.Fatalf("PruneLegacyCrewResources: %v", err)
	}

	// Container: only the legacy one (by its container Id), never the id-scoped.
	if len(delC) != 1 || delC[0] != "legacyteam" {
		t.Errorf("container deletes = %v; want [legacyteam] only (id-scoped must survive)", delC)
	}
	// Volumes: only the two legacy names, never the id-scoped siblings.
	sort.Strings(delV)
	wantV := []string{"crewship-home-engineering", "crewship-tools-engineering"}
	if strings.Join(delV, ",") != strings.Join(wantV, ",") {
		t.Errorf("volume deletes = %v; want %v (id-scoped must survive)", delV, wantV)
	}
	// Returned names cover all three legacy resources.
	sort.Strings(removed)
	want := []string{"crewship-home-engineering", "crewship-team-engineering", "crewship-tools-engineering"}
	if strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed = %v; want %v", removed, want)
	}
}

func TestPruneLegacyCrewResources_VolumeFailureNonFatal(t *testing.T) {
	t.Parallel()

	// A volume that won't delete (in use) must not abort the prune or surface
	// as a fatal error — mirrors RemoveCrewVolumes' log-and-continue posture.
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Volumes": []map[string]any{{"Name": "crewship-tools-engineering"}},
			})
		case r.Method == http.MethodDelete:
			http.Error(w, `{"message":"volume is in use"}`, http.StatusConflict)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	removed, err := p.PruneLegacyCrewResources(context.Background(), []string{"engineering"})
	if err != nil {
		t.Errorf("per-resource failure must not propagate: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("failed delete should not be reported as removed: %v", removed)
	}
}

func TestPruneLegacyCrewResources_EmptySlugNoop(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("empty slug must not touch the daemon: %s %s", r.Method, r.URL.Path)
	})
	defer cleanup()

	removed, err := p.PruneLegacyCrewResources(context.Background(), []string{""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v; want none", removed)
	}
}

// Provider must satisfy the optional pruner interface so the API layer can
// type-assert it.
var _ provider.LegacyResourcePruner = (*Provider)(nil)
