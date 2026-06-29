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

// legacyDaemonHandler mimics the slice of the Docker REST API the legacy
// detect/prune paths touch: container list + remove, volume list + remove. The
// fixture daemon holds, for slug "engineering": the three legacy slug-only
// resources AND their id-scoped (post-C1) siblings, which must be left
// untouched. deletedContainers/deletedVolumes record what prune targeted.
func legacyDaemonHandler(t *testing.T, prefix string, deletedContainers, deletedVolumes *[]string) http.HandlerFunc {
	t.Helper()
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
	p, cleanup := newFakeDockerProvider(t, legacyDaemonHandler(t, "crewship", &delC, &delV))
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	removed, err := p.PruneLegacyCrewResources(context.Background(),
		[]provider.CrewRef{{ID: "crew1", Slug: "engineering"}})
	if err != nil {
		t.Fatalf("PruneLegacyCrewResources: %v", err)
	}

	// Container: only the legacy one (by container Id), never the id-scoped.
	if len(delC) != 1 || delC[0] != "legacyteam" {
		t.Errorf("container deletes = %v; want [legacyteam] only (id-scoped must survive)", delC)
	}
	sort.Strings(delV)
	wantV := []string{"crewship-home-engineering", "crewship-tools-engineering"}
	if strings.Join(delV, ",") != strings.Join(wantV, ",") {
		t.Errorf("volume deletes = %v; want %v (id-scoped must survive)", delV, wantV)
	}
	sort.Strings(removed)
	want := []string{"crewship-home-engineering", "crewship-team-engineering", "crewship-tools-engineering"}
	if strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Errorf("removed = %v; want %v", removed, want)
	}
}

// TestPruneLegacyCrewResources_ProtectsCollidingLiveCrew pins the slug/id
// collision guard: a crew whose slug equals another crew's "<slug>-<id>" string
// produces a legacy key identical to that crew's LIVE id-scoped name. Prune
// must NOT delete it.
func TestPruneLegacyCrewResources_ProtectsCollidingLiveCrew(t *testing.T) {
	t.Parallel()

	var delC, delV []string
	p, cleanup := newFakeDockerProvider(t, legacyDaemonHandler(t, "crewship", &delC, &delV))
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	// Crew A has slug "engineering-crew1" — its legacy name collides with live
	// crew B's id-scoped name (slug "engineering", id "crew1"). Both present.
	removed, err := p.PruneLegacyCrewResources(context.Background(), []provider.CrewRef{
		{ID: "crewA", Slug: "engineering-crew1"},
		{ID: "crew1", Slug: "engineering"},
	})
	if err != nil {
		t.Fatalf("PruneLegacyCrewResources: %v", err)
	}
	for _, n := range removed {
		if strings.HasSuffix(n, "-crew1") {
			t.Errorf("prune removed a live id-scoped resource %q (collision not protected)", n)
		}
	}
}

func TestPruneLegacyCrewResources_VolumeFailureNonFatal(t *testing.T) {
	t.Parallel()

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

	removed, err := p.PruneLegacyCrewResources(context.Background(),
		[]provider.CrewRef{{Slug: "engineering"}})
	if err != nil {
		t.Errorf("per-resource failure must not propagate: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("failed delete should not be reported as removed: %v", removed)
	}
}

func TestPruneLegacyCrewResources_NoCrewsNoop(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("empty crew set must not touch the daemon: %s %s", r.Method, r.URL.Path)
	})
	defer cleanup()

	removed, err := p.PruneLegacyCrewResources(context.Background(), []provider.CrewRef{{Slug: ""}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v; want none", removed)
	}
}

func TestHasLegacyCrewResources(t *testing.T) {
	t.Parallel()

	var delC, delV []string // unused; detection never deletes
	p, cleanup := newFakeDockerProvider(t, legacyDaemonHandler(t, "crewship", &delC, &delV))
	defer cleanup()
	p.cfg.ContainerPrefix = "crewship"

	// Legacy present for "engineering".
	present, err := p.HasLegacyCrewResources(context.Background(),
		[]provider.CrewRef{{ID: "crew1", Slug: "engineering"}})
	if err != nil {
		t.Fatalf("HasLegacyCrewResources: %v", err)
	}
	if !present {
		t.Error("expected present=true for slug with legacy resources")
	}
	if len(delC)+len(delV) != 0 {
		t.Errorf("detection must not delete anything: containers=%v volumes=%v", delC, delV)
	}

	// A slug with no legacy resources → clean.
	clean, err := p.HasLegacyCrewResources(context.Background(),
		[]provider.CrewRef{{ID: "x", Slug: "marketing"}})
	if err != nil {
		t.Fatalf("HasLegacyCrewResources: %v", err)
	}
	if clean {
		t.Error("expected present=false for slug without legacy resources")
	}
}

// Provider must satisfy both optional interfaces so the API layer can
// type-assert it.
var (
	_ provider.LegacyResourcePruner   = (*Provider)(nil)
	_ provider.LegacyResourceDetector = (*Provider)(nil)
)
