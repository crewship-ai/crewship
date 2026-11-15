package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type serviceImageAcquirer struct {
	held *atomic.Bool
	id   string
}

func (a serviceImageAcquirer) AcquireImageReferences(_ context.Context, refs []string) (map[string]string, func(), error) {
	a.held.Store(true)
	return map[string]string{refs[0]: a.id}, func() { a.held.Store(false) }, nil
}

func TestServiceControllerAndStarterKeepDeclaredImageAndReuseResolvedArtifact(t *testing.T) {
	svc := covRedisSvc()
	id := "sha256:" + strings.Repeat("a", 64)
	hash := computeSidecarSpecHash(&svc)
	var held atomic.Bool
	var lists atomic.Int64
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if !held.Load() {
			t.Error("service engine operation escaped artifact acquisition lifetime")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			lists.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id": "cid-redis", "Names": []string{"/crewship-svc-alpha-ckalpha0001-redis"},
				"Image": id, "ImageID": id, "State": "running",
				"Labels": map[string]string{sidecarSpecHashLabel: hash},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
			_, _ = w.Write([]byte(`{"Volumes":[],"Warnings":null}`))
		default:
			t.Errorf("matching exact artifact must not be pulled/recreated: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	p.SetImageReferenceAcquirer(serviceImageAcquirer{held: &held, id: id})
	declaration := provider.CrewConfig{ID: covCrewID, Slug: "alpha", Services: []provider.CrewService{svc}}
	for range 2 { // the starter and direct controller enter the same provider port
		ids, err := p.EnsureCrewServices(t.Context(), declaration)
		if err != nil || ids["redis"] != "cid-redis" || held.Load() {
			t.Fatalf("service reuse failed: %v %v held=%t", ids, err, held.Load())
		}
	}
	if lists.Load() < 2 || declaration.Services[0].Image != svc.Image || declaration.Services[0].ResolvedImage != "" {
		t.Fatal("provider mutated caller declaration or did not exercise both starts")
	}
}

func TestServiceQuotaInspectsTheSameResolvedImageItWillCreate(t *testing.T) {
	id := "sha256:" + strings.Repeat("b", 64)
	inspected := false
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/images/"+id+"/json") {
			t.Errorf("quota inspected mutable alias rather than retained image: %s", r.URL.Path)
			http.Error(w, "wrong identity", http.StatusBadRequest)
			return
		}
		inspected = true
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Config": map[string]any{"User": "1001:1001"}})
	})
	owner, err := p.quotaImageOwner(t.Context(), &provider.CrewService{Name: "service", Image: "mutable:latest", ResolvedImage: id})
	if err != nil || !inspected || !owner.Set || owner.UID != 1001 {
		t.Fatalf("quota admission used wrong image: %+v %v", owner, err)
	}
}
