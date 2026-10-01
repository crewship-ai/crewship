package docker

import (
	"context"
	"errors"
	"net/http"
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
