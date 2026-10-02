package crewstart

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// A routine started against a crew whose cached image was evicted (or never
// built) failed with "cached devcontainer image missing locally": only chat,
// assignments, the query handler and `crew start` waited for the build. The
// gate sits in Start so the pipeline, the scheduler, webhooks, the terminal
// and the orchestrator wait for it too.

func setGate(t *testing.T, gate ImageGate) {
	t.Helper()
	SetImageGate(gate)
	t.Cleanup(func() { SetImageGate(nil) })
}

func TestStartWaitsForTheImageGateBeforeTheRuntime(t *testing.T) {
	f := &fakeRuntime{}
	var gated string
	setGate(t, func(_ context.Context, crewID string) error {
		gated = crewID
		f.calls = append(f.calls, "gate")
		return nil
	})

	if _, err := New(f, nil, nil).Start(context.Background(), provider.CrewConfig{ID: "crew-9", Slug: "s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if gated != "crew-9" {
		t.Errorf("gate saw crew %q, want crew-9", gated)
	}
	if len(f.calls) != 2 || f.calls[0] != "gate" || f.calls[1] != "runtime" {
		t.Errorf("calls = %v, want [gate runtime]", f.calls)
	}
}

func TestStartGateRunsBeforeTheCompleterReadsTheImage(t *testing.T) {
	// The rebuild writes crews.cached_image; the completer must read it after.
	var order []string
	setGate(t, func(context.Context, string) error {
		order = append(order, "gate")
		return nil
	})
	completer := CompleterFunc(func(_ context.Context, c provider.CrewConfig) (provider.CrewConfig, error) {
		order = append(order, "complete")
		return c, nil
	})
	if _, err := New(&fakeRuntime{}, completer, nil).Start(context.Background(), provider.CrewConfig{ID: "c", Slug: "s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(order) != 2 || order[0] != "gate" {
		t.Errorf("order = %v, want gate before complete", order)
	}
}

func TestStartFailsWithoutTouchingTheRuntimeWhenTheGateFails(t *testing.T) {
	f := &fakeRuntime{}
	buildErr := errors.New("provisioning crew c failed: npm ERR")
	setGate(t, func(context.Context, string) error { return buildErr })

	_, err := New(f, nil, nil).Start(context.Background(), provider.CrewConfig{ID: "c", Slug: "s"})
	if !errors.Is(err, buildErr) || !errors.Is(err, ErrImageNotReady) {
		t.Fatalf("err = %v, want ErrImageNotReady wrapping the build error", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("runtime was started anyway: %v", f.calls)
	}
}

func TestStartWithoutAGateOrCrewIDStartsAsBefore(t *testing.T) {
	f := &fakeRuntime{}
	if _, err := New(f, nil, nil).Start(context.Background(), provider.CrewConfig{ID: "c", Slug: "s"}); err != nil {
		t.Fatalf("no gate: %v", err)
	}
	called := false
	setGate(t, func(context.Context, string) error { called = true; return nil })
	if _, err := New(f, nil, nil).Start(context.Background(), provider.CrewConfig{Slug: "s"}); err != nil {
		t.Fatalf("no crew id: %v", err)
	}
	if called {
		t.Error("gate consulted for a config without a crew id")
	}
}

func TestStartTakesTheRebuiltImageOverTheCallersStaleOne(t *testing.T) {
	// The pipeline resolves its config before Start; a rebuild for a new
	// adapter writes a new tag, and the caller's copy must not win.
	f := &fakeRuntime{}
	setGate(t, func(context.Context, string) error { return nil })
	completer := CompleterFunc(func(_ context.Context, c provider.CrewConfig) (provider.CrewConfig, error) {
		c.CachedImage = "crewship-cache:new"
		return c, nil
	})
	cfg := provider.CrewConfig{ID: "c", Slug: "s", CachedImage: "crewship-cache:old"}
	if _, err := New(f, completer, nil).Start(context.Background(), cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.runtimeCfg.CachedImage != "crewship-cache:new" {
		t.Errorf("runtime started from %q, want the rebuilt crewship-cache:new", f.runtimeCfg.CachedImage)
	}
}

func TestStartWithoutACompleterKeepsTheCallersImage(t *testing.T) {
	// Chat resolves its own config and has no completer to re-read from.
	f := &fakeRuntime{}
	setGate(t, func(context.Context, string) error { return nil })
	cfg := provider.CrewConfig{ID: "c", Slug: "s", CachedImage: "crewship-cache:chat"}
	if _, err := New(f, nil, nil).Start(context.Background(), cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.runtimeCfg.CachedImage != "crewship-cache:chat" {
		t.Errorf("runtime started from %q, want the caller's image", f.runtimeCfg.CachedImage)
	}
}

func TestStartRefusesTheCallersStaleImageWhenTheCompleterFailsAfterTheGate(t *testing.T) {
	// The gate may have rebuilt under a new tag; the caller's tag is
	// unverified. Without the current one, nothing starts.
	f := &fakeRuntime{}
	setGate(t, func(context.Context, string) error { return nil })
	completer := CompleterFunc(func(context.Context, provider.CrewConfig) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, errors.New("database is locked")
	})
	cfg := provider.CrewConfig{ID: "c", Slug: "s", CachedImage: "crewship-cache:old"}
	_, err := New(f, completer, nil).Start(context.Background(), cfg)
	if !errors.Is(err, ErrImageNotReady) {
		t.Fatalf("err = %v, want ErrImageNotReady", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("runtime started from the stale image: %v", f.calls)
	}
}

func TestStartRefusesAnUnverifiedConfigAfterTheGateEvenWithoutACallerImage(t *testing.T) {
	// Without a tag of its own the caller would start the default runtime
	// image — still not the image the gate just verified.
	f := &fakeRuntime{}
	setGate(t, func(context.Context, string) error { return nil })
	readErr := errors.New("database is locked")
	completer := CompleterFunc(func(context.Context, provider.CrewConfig) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, readErr
	})
	_, err := New(f, completer, nil).Start(context.Background(), provider.CrewConfig{ID: "c", Slug: "s"})
	if !errors.Is(err, ErrImageNotReady) || !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want ErrImageNotReady wrapping the completer error", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("runtime started from an unverified config: %v", f.calls)
	}
}

func TestStartAfterTheGateKeepsAPartialCompletionWithItsImage(t *testing.T) {
	// Undecodable services leave the image, mounts and limits valid: the
	// crew starts from them, without the services.
	f := &fakeRuntime{}
	setGate(t, func(context.Context, string) error { return nil })
	completer := CompleterFunc(func(_ context.Context, c provider.CrewConfig) (provider.CrewConfig, error) {
		return provider.CrewConfig{ID: c.ID, Slug: "s", CachedImage: "crewship-cache:new"},
			fmt.Errorf("%w: decode services_json", PartialConfigError("crew sidecar services unresolved"))
	})
	if _, err := New(f, completer, nil).Start(context.Background(), provider.CrewConfig{ID: "c", CachedImage: "crewship-cache:old"}); err != nil {
		t.Fatalf("a partial completion must not fail the start: %v", err)
	}
	if f.runtimeCfg.CachedImage != "crewship-cache:new" {
		t.Errorf("runtime started from %q, want crewship-cache:new", f.runtimeCfg.CachedImage)
	}
}

func TestStartAfterTheGateStartsACrewThatNeedsNoImage(t *testing.T) {
	// A crew that needs no build and was never provisioned has no cached
	// image; the completer says so successfully and the default image is right.
	f := &fakeRuntime{}
	setGate(t, func(context.Context, string) error { return nil })
	completer := CompleterFunc(func(_ context.Context, c provider.CrewConfig) (provider.CrewConfig, error) {
		return provider.CrewConfig{ID: c.ID, Slug: "s"}, nil
	})
	if _, err := New(f, completer, nil).Start(context.Background(), provider.CrewConfig{ID: "c"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.runtimeCfg.CachedImage != "" {
		t.Errorf("CachedImage = %q, want empty (default image)", f.runtimeCfg.CachedImage)
	}
}
