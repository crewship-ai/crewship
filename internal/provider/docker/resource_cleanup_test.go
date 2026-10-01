package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestCleanupRemovePreservesVolumesFakeAPI(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || !strings.HasSuffix(r.URL.Path, "/containers/immutable-id") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("v") == "1" || r.URL.Query().Get("v") == "true" || r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true" {
			t.Error("destructive remove flags")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer close()
	rt := &cleanupRuntime{client: p.client}
	if err := rt.Remove(context.Background(), "immutable-id"); err != nil {
		t.Fatal(err)
	}
}
func TestInstanceLabelsOverrideImageIdentity(t *testing.T) {
	for _, id := range []string{"installation-id", ""} {
		labels := resourcelifecycle.WithInstanceLabel(sidecarContainerLabels("crew-id", "slug", "postgres", "hash"), id)
		if labels[resourcelifecycle.InstanceLabel] != id || labels[crewCrewIDLabel] != "crew-id" {
			t.Fatalf("%+v", labels)
		}
	}
}

func TestRuntimeInstanceLabelsDoNotChangeContract(t *testing.T) {
	f := &covRT{}
	cfg := covRTConfig(t)
	p := f.provider(t, cfg)
	before := p.crewRuntimeContractDigest()
	p.cfg.InstanceID = "installation-a"
	// A fresh provider computes the same contract: attribution alone cannot
	// trigger forceTeardown of an existing legacy container.
	f2 := &covRT{}
	cfg.InstanceID = "installation-a"
	p2 := f2.provider(t, cfg)
	if got := p2.crewRuntimeContractDigest(); got != before {
		t.Fatalf("label-only contract drift %s -> %s", before, got)
	}
	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatal(err)
	}
	if got := f.realCreate(t).Config.Labels[resourcelifecycle.InstanceLabel]; got != "installation-a" {
		t.Fatalf("create instance label %q", got)
	}
}

// A server whose own identity is empty (cleanup disabled: second live holder,
// unstable database location) must still refuse: the image-drift path would
// otherwise tear the other installation's runtime down with RemoveVolumes.
func TestForeignInstanceRuntimeNeverReusedOrRemoved(t *testing.T) {
	for _, local := range []string{"installation-a", ""} {
		t.Run("local="+local, func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("foreign mutation %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if strings.HasSuffix(r.URL.Path, "/containers/json") {
					w.Write([]byte(`[{"Id":"foreign-container","Names":["/same-name"]}]`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/containers/foreign-container/json") {
					w.Write([]byte(`{"Id":"foreign-container","Config":{"Image":"drifted-image","Labels":{"crewship.instance-id":"installation-b"}}}`))
					return
				}
				t.Errorf("unexpected %s", r.URL.Path)
			})
			defer close()
			p.cfg.InstanceID = local
			if _, _, err := p.reconcileExistingContainer(context.Background(), provider.CrewConfig{ID: "crew"}, "same-name", "image", false, func(devcontainer.ProvisionEvent) {}); err == nil {
				t.Fatal("foreign reuse accepted")
			}
		})
	}
}

func TestServiceCreateCarriesInstanceLabelFakeAPI(t *testing.T) {
	var labels map[string]string
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			w.Write([]byte(`{"Id":"sha256:local"}`))
		case strings.Contains(r.URL.Path, "/images/create"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			var body struct{ Labels map[string]string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			labels = body.Labels
			w.Write([]byte(`{"Id":"new-service"}`))
		case strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer close()
	p.cfg.InstanceID = "installation-a"
	if _, err := p.ensureSidecar(context.Background(), "crew-id", "slug", &provider.CrewService{Name: "redis", Image: "redis:7"}); err != nil {
		t.Fatal(err)
	}
	if labels[resourcelifecycle.InstanceLabel] != "installation-a" || labels[crewCrewIDLabel] != "crew-id" {
		t.Fatalf("create labels %+v", labels)
	}
}

func TestDeletedOwnerAdmissionPreventsProviderWork(t *testing.T) {
	p := &Provider{cfg: Config{OwnerActive: func(context.Context, string) error { return fmt.Errorf("deleted") }}}
	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "deleted"}); err == nil {
		t.Fatal("runtime admission allowed deleted owner")
	}
	if _, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{ID: "deleted"}); err == nil {
		t.Fatal("service admission allowed deleted owner")
	}
}

func TestCleanupMalformedInspectFailsWithoutPanic(t *testing.T) {
	for _, body := range []string{`{"Id":"container"}`, `{"Config":{"Labels":{}}}`} {
		t.Run(body, func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
			defer close()
			if _, err := (&cleanupRuntime{client: p.client}).Inspect(context.Background(), "container"); err == nil {
				t.Fatal("incomplete inspect accepted")
			}
		})
	}
}
