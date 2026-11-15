package crewstart

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type imageLeaseRuntime struct {
	fakeRuntime
	t    *testing.T
	held *bool
}

func (f *imageLeaseRuntime) EnsureCrewRuntime(ctx context.Context, cfg provider.CrewConfig) (string, error) {
	if !*f.held || cfg.CachedImage != "resolved-exact-image" {
		f.t.Fatal("runtime consumed an unleased or unresolved image")
	}
	return f.fakeRuntime.EnsureCrewRuntime(ctx, cfg)
}

func TestStartHoldsImageAcquisitionThroughContainerCreationAndReleasesOnError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		SetImageGate(nil)
		held, released := false, 0
		SetImageAcquirer(func(_ context.Context, cfg provider.CrewConfig) (provider.CrewConfig, func(), error) {
			held = true
			cfg.CachedImage = "resolved-exact-image"
			return cfg, func() { held = false; released++ }, nil
		})
		t.Cleanup(func() { SetImageAcquirer(nil) })
		runtime := &imageLeaseRuntime{t: t, held: &held}
		if fail {
			runtime.ensureErr = errors.New("container create unavailable")
		}
		_, err := New(runtime, nil, nil).Start(t.Context(), provider.CrewConfig{ID: "crew", CachedImage: "mutable-alias"})
		if (err != nil) != fail || held || released != 1 {
			t.Fatalf("lease lifetime: failure=%t error=%v held=%t released=%d", fail, err, held, released)
		}
	}
}

func TestStartDoesNotCreateRuntimeWhenExactImageAcquisitionFails(t *testing.T) {
	SetImageGate(nil)
	SetImageAcquirer(func(_ context.Context, cfg provider.CrewConfig) (provider.CrewConfig, func(), error) {
		return cfg, nil, errors.New("selected image unavailable")
	})
	t.Cleanup(func() { SetImageAcquirer(nil) })
	runtime := &fakeRuntime{}
	if _, err := New(runtime, nil, nil).Start(t.Context(), provider.CrewConfig{ID: "crew"}); err == nil || len(runtime.calls) != 0 {
		t.Fatal("runtime started after failed image acquisition")
	}
}
