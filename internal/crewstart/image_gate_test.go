package crewstart

import (
	"context"
	"errors"
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
