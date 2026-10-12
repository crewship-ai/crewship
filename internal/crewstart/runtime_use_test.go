package crewstart

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type reservedRuntime struct {
	*sidecarRuntime
	gate provider.RuntimeUseGate
}

func (r *reservedRuntime) AcquireCrewRuntimeUse(ctx context.Context, cfg provider.CrewConfig) (*provider.RuntimeUse, error) {
	release, err := r.gate.Use(ctx)
	if err != nil {
		return nil, err
	}
	r.runtimeCfg = cfg
	r.calls = append(r.calls, "reserved")
	return provider.NewRuntimeUse(cfg.ID, "reserved-container", release), nil
}

func (r *reservedRuntime) RetainCrewRuntimeUse(context.Context, string, string) (*provider.RuntimeUse, error) {
	return nil, errors.New("unexpected retain")
}

func TestStartUseRetainsRuntimeAcrossSidecarFailure(t *testing.T) {
	f := &fakeRuntime{servicesErr: errors.New("service unavailable")}
	r := &reservedRuntime{sidecarRuntime: &sidecarRuntime{fakeRuntime: f}}
	s := New(r, nil, nil)
	use, _, err := s.StartUse(t.Context(), redisConfig(), nil)
	if !errors.Is(err, ErrSidecarStart) || use == nil || use.ContainerID() != "reserved-container" {
		t.Fatalf("lost reserved runtime on partial startup: use=%v err=%v", use, err)
	}
	defer use.Release()
	if len(f.calls) != 2 || f.calls[0] != "reserved" || f.calls[1] != "services" {
		t.Fatalf("startup bypassed reservation: %v", f.calls)
	}
	if unlock, ok := r.gate.TryExclusive(); ok {
		unlock()
		t.Fatal("partial startup prematurely released runtime")
	}
	use.Release()
	if unlock, ok := r.gate.TryExclusive(); !ok {
		t.Fatal("caller could not release partial startup")
	} else {
		unlock()
	}
}

func TestStartUseWithoutProvider(t *testing.T) {
	var s *Starter
	use, _, err := s.StartUse(t.Context(), provider.CrewConfig{ID: "crew"}, nil)
	if use != nil || !errors.Is(err, ErrNoContainerProvider) {
		t.Fatalf("missing provider: use=%v err=%v", use, err)
	}
}
