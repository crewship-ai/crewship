package docker

// Tests for ListCrewContainers — the whole-crew container inventory
// (GET /api/v1/crews/{crewId}/containers, #1697). Uses the fake HTTP API
// harness (fakeapi_test.go) so it runs on a pure Go test, no live daemon.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestMatchCrewContainer is the classification table. Every row is a
// container the daemon might hand back; the question is whether it belongs
// to crew "ckalpha01" and, if so, as what.
func TestMatchCrewContainer(t *testing.T) {
	t.Parallel()

	const crewID = "ckalpha01"
	const crewName = "crewship-team-alpha-ckalpha01"

	tests := []struct {
		name       string
		labels     map[string]string
		names      []string
		crewID     string
		wantKind   string
		wantMatch  bool
		wantReason string
	}{
		{
			name:      "the crew's own labelled runtime container",
			labels:    map[string]string{crewCrewIDLabel: crewID, crewKindLabel: crewRuntimeKind},
			names:     []string{"/" + crewName},
			crewID:    crewID,
			wantKind:  provider.CrewContainerKindCrew,
			wantMatch: true,
		},
		{
			name:      "the crew's sidecar",
			labels:    map[string]string{crewCrewIDLabel: crewID, crewKindLabel: sidecarKind, sidecarSvcLabel: "redis"},
			names:     []string{"/crewship-svc-alpha-ckalpha01-redis"},
			crewID:    crewID,
			wantKind:  provider.CrewContainerKindSidecar,
			wantMatch: true,
		},
		{
			name:       "an unlabelled runtime container matched by exact name",
			labels:     map[string]string{},
			names:      []string{"/" + crewName},
			crewID:     crewID,
			wantKind:   provider.CrewContainerKindCrew,
			wantMatch:  true,
			wantReason: "containers created before the ownership labels existed still belong to the crew",
		},
		{
			name:       "another crew's runtime container",
			labels:     map[string]string{crewCrewIDLabel: "ckbeta02", crewKindLabel: crewRuntimeKind},
			names:      []string{"/crewship-team-beta-ckbeta02"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "cross-tenant leak (#1732)",
		},
		{
			name:       "another crew's sidecar",
			labels:     map[string]string{crewCrewIDLabel: "ckbeta02", crewKindLabel: sidecarKind, sidecarSvcLabel: "redis"},
			names:      []string{"/crewship-svc-beta-ckbeta02-redis"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "cross-tenant leak (#1732)",
		},
		{
			name: "an identically-slugged crew in another workspace",
			// crews is UNIQUE(workspace_id, slug), so the slug label is
			// byte-identical to alpha's while the crew id is not.
			labels:     map[string]string{crewCrewIDLabel: "ckotherws03", crewCrewLabel: "alpha", crewKindLabel: crewRuntimeKind},
			names:      []string{"/crewship-team-alpha-ckotherws03"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "the slug is not an identity (#1732)",
		},
		{
			name:       "a container whose name merely starts with the crew's name",
			labels:     map[string]string{},
			names:      []string{"/" + crewName + "-scratch"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "the name fallback is equality, never a prefix",
		},
		{
			name:       "an unlabelled container named for a DIFFERENT crew",
			labels:     map[string]string{},
			names:      []string{"/crewship-team-beta-ckbeta02"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "the fallback name is derived from this crew's id",
		},
		{
			// The label set as it stood before crewship.kind existed: the
			// crew id and slug, no kind. service_inventory_test.go's own
			// fixture for "the crew's own agent runtime" is shaped exactly
			// this way, so it is not hypothetical.
			name:       "this crew's id with no kind label, name matching",
			labels:     map[string]string{crewCrewIDLabel: crewID, crewCrewLabel: "alpha"},
			names:      []string{"/" + crewName},
			crewID:     crewID,
			wantKind:   provider.CrewContainerKindCrew,
			wantMatch:  true,
			wantReason: "a partially-labelled container is a gap, not a conflict",
		},
		{
			name:       "a container labelled for another crew but named like this one",
			labels:     map[string]string{crewCrewIDLabel: "ckbeta02"},
			names:      []string{"/" + crewName},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "a present-but-wrong ownership label is a conflict, not a gap",
		},
		{
			name:       "a sidecar VOLUME's labels (kind is not a container kind)",
			labels:     map[string]string{crewCrewIDLabel: crewID, crewKindLabel: sidecarVolumeKind},
			names:      []string{"/whatever"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "only crew + sidecar kinds are containers",
		},
		{
			name:       "an unrelated container on the same host",
			labels:     map[string]string{},
			names:      []string{"/my-personal-postgres"},
			crewID:     crewID,
			wantMatch:  false,
			wantReason: "nothing ties it to any crew",
		},
		{
			name:       "an empty crew id matches nothing",
			labels:     map[string]string{crewCrewIDLabel: crewID, crewKindLabel: crewRuntimeKind},
			names:      []string{"/" + crewName},
			crewID:     "",
			wantMatch:  false,
			wantReason: "a caller that could not resolve the crew must see nothing, not everything",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// crewContainerName is only derivable for the crew under test.
			name := crewName
			if tt.crewID == "" {
				name = ""
			}
			kind, matched := matchCrewContainer(tt.labels, tt.names, tt.crewID, name)
			if matched != tt.wantMatch {
				t.Fatalf("matched = %v, want %v (%s)", matched, tt.wantMatch, tt.wantReason)
			}
			if kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", kind, tt.wantKind)
			}
		})
	}
}

// TestListCrewContainers_RuntimeAndSidecars is the load-bearing case: the
// crew's own runtime container — the row the Docker tab is mostly about,
// and the one ListCrewServices can never return — comes back alongside the
// sidecars, with live state, and nothing belonging to another crew does.
func TestListCrewContainers_RuntimeAndSidecars(t *testing.T) {
	t.Parallel()

	// ListCrewContainers does a ContainerList followed by a ContainerStatus
	// inspect per match, so the fake answers both.
	inspectState := map[string]map[string]any{
		"team-cid":  {"Running": true, "StartedAt": "2026-01-01T00:00:00Z"},
		"redis-cid": {"Running": true, "StartedAt": "2026-01-01T00:00:00Z"},
		"pg-cid":    {"StartedAt": "2026-01-01T00:00:00Z"}, // no Running flag => stopped
	}
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"Id":     "team-cid",
					"Names":  []string{"/crewship-team-alpha-ckalpha01"},
					"Image":  "crewship/agent:latest",
					"State":  "running",
					"Labels": map[string]any{"crewship.crew-id": "ckalpha01", "crewship.crew": "alpha", "crewship.kind": "crew"},
				},
				{
					"Id":     "redis-cid",
					"Names":  []string{"/crewship-svc-alpha-ckalpha01-redis"},
					"Image":  "redis:7-alpine",
					"State":  "running",
					"Labels": map[string]any{"crewship.crew-id": "ckalpha01", "crewship.crew": "alpha", "crewship.kind": "sidecar", "crewship.svc": "redis"},
				},
				{
					"Id":     "pg-cid",
					"Names":  []string{"/crewship-svc-alpha-ckalpha01-postgres"},
					"Image":  "postgres:16",
					"State":  "exited",
					"Labels": map[string]any{"crewship.crew-id": "ckalpha01", "crewship.crew": "alpha", "crewship.kind": "sidecar", "crewship.svc": "postgres"},
				},
				{
					// Another workspace's identically-slugged crew.
					"Id":     "twin-cid",
					"Names":  []string{"/crewship-team-alpha-ckotherws03"},
					"Image":  "crewship/agent:old",
					"State":  "running",
					"Labels": map[string]any{"crewship.crew-id": "ckotherws03", "crewship.crew": "alpha", "crewship.kind": "crew"},
				},
			})
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/containers/"):], "/containers/"), "/json")
			state, ok := inspectState[id]
			if !ok {
				t.Errorf("unexpected inspect for container id %q — a foreign crew's container was reached", id)
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

	got, err := p.ListCrewContainers(context.Background(), "ckalpha01", "alpha")
	if err != nil {
		t.Fatalf("ListCrewContainers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 containers (runtime + 2 sidecars), got %d: %+v", len(got), got)
	}

	byName := map[string]provider.CrewContainerInfo{}
	for _, c := range got {
		byName[c.Name] = c
		if c.Image == "crewship/agent:old" {
			t.Errorf("cross-tenant leak: the same-slug crew in another workspace surfaced (%+v)", c)
		}
	}

	runtime, ok := byName["crewship-team-alpha-ckalpha01"]
	if !ok {
		t.Fatalf("the crew's own runtime container is missing from %+v — that is the #1697 bug", got)
	}
	if runtime.Kind != provider.CrewContainerKindCrew {
		t.Errorf("runtime kind = %q, want %q", runtime.Kind, provider.CrewContainerKindCrew)
	}
	if runtime.Image != "crewship/agent:latest" || runtime.State != "running" {
		t.Errorf("runtime = %+v", runtime)
	}

	pg, ok := byName["crewship-svc-alpha-ckalpha01-postgres"]
	if !ok {
		t.Fatalf("missing postgres sidecar in %+v", got)
	}
	if pg.Kind != provider.CrewContainerKindSidecar {
		t.Errorf("postgres kind = %q, want %q", pg.Kind, provider.CrewContainerKindSidecar)
	}
	// The point of inspecting: a stopped container reports live state, not
	// the "configured" fiction a DB snapshot would give.
	if pg.State != "stopped" {
		t.Errorf("postgres state = %q, want stopped", pg.State)
	}
}

// TestListCrewContainers_NoCrewID_Errors pins the refusal: an unresolved
// crew id must not be answered with "here is everything on the daemon".
func TestListCrewContainers_NoCrewID_Errors(t *testing.T) {
	t.Parallel()

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("daemon must not be reached without a crew id (%s)", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer cleanup()

	if _, err := p.ListCrewContainers(context.Background(), "", "alpha"); err == nil {
		t.Fatal("expected an error for an empty crew id, got nil")
	}
}

// TestProviderImplementsCrewContainerLister pins the wiring: the API layer
// reaches this code through a type assertion, which fails silently (empty
// list) if the method signature ever drifts from the interface.
func TestProviderImplementsCrewContainerLister(t *testing.T) {
	t.Parallel()
	var _ provider.CrewContainerLister = (*Provider)(nil)
}
