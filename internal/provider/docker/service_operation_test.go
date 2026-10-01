package docker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestServiceStartRechecksMaintenanceAfterDelayedCreation(t *testing.T) {
	var requests atomic.Int32
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) })
	defer close()
	maintenance := errors.New("fresh maintenance epoch owns crew")
	p.SetServiceOperationGate(provider.ServiceOperationGate(func(context.Context, string) (func(context.Context) error, error) { return nil, maintenance }))
	if err := p.startAdmittedService(context.Background(), "crew", "newly-created-exact-id"); !errors.Is(err, maintenance) {
		t.Fatalf("lost maintenance rejection: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("Docker Start reached wire after old lease expired and maintenance adopted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.SetServiceOperationGate(nil)
	if err := p.startAdmittedService(ctx, "crew", "late-created-id"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("late Create response bypassed canceled operation context")
	}
}

func TestServiceImagePullPrecedesBoundedMutationAdmission(t *testing.T) {
	var pulls atomic.Int32
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			pulls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}\n"))
		case strings.Contains(r.URL.Path, "/images/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"image"}`))
		default:
			t.Errorf("service mutation before admission: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer close()
	maintenance := errors.New("maintenance")
	p.SetServiceOperationGate(provider.ServiceOperationGate(func(ctx context.Context, _ string) (func(context.Context) error, error) {
		if pulls.Load() != 1 {
			t.Errorf("image not ready before operation lease: %d", pulls.Load())
		}
		return nil, maintenance
	}))
	_, err := p.EnsureCrewServices(context.Background(), provider.CrewConfig{ID: "crew", Slug: "crew", Services: []provider.CrewService{{Name: "database", Image: "test-image"}}})
	if !errors.Is(err, maintenance) {
		t.Fatal(err)
	}
}
