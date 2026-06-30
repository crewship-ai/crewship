package docker

// Fix 3 regression: the agent-run / ensure-container path must journal its
// container-preparation steps. EnsureCrewRuntime emits ProvisionEvents to the
// optional CrewConfig.ProvisionSink so the runtime container the agent runs in
// is auditable exactly like the explicit image-build job. Mirrors the build
// pipeline's observability test (internal/devcontainer/provisioner_observability_test.go).

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

func hasProvStep(evs []devcontainer.ProvisionEvent, step, status string) bool {
	for _, e := range evs {
		if e.Step == step && (status == "" || e.Status == status) {
			return true
		}
	}
	return false
}

// TestEnsureCrewRuntime_Sink_EmitsCreatePathEvents proves the create path emits
// the ordered audit trail start → container_create → ready, every event carries
// the canonical provision phase, and nothing reaches failed.
func TestEnsureCrewRuntime_Sink_EmitsCreatePathEvents(t *testing.T) {
	p, _ := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	var got []devcontainer.ProvisionEvent
	sink := func(ev devcontainer.ProvisionEvent) { got = append(got, ev) }

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:            "crew-sink",
		Slug:          "eng",
		ProvisionSink: sink,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	if !hasProvStep(got, devcontainer.ProvStepStart, "") {
		t.Errorf("missing provision.start event: %+v", got)
	}
	if !hasProvStep(got, devcontainer.ProvStepContainerCreate, devcontainer.ProvStatusCompleted) {
		t.Errorf("missing container_create{completed} event: %+v", got)
	}
	if !hasProvStep(got, devcontainer.ProvStepReady, devcontainer.ProvStatusCompleted) {
		t.Errorf("missing ready{completed} event: %+v", got)
	}
	if hasProvStep(got, devcontainer.ProvStepFailed, "") {
		t.Errorf("happy path must not emit provision.failed: %+v", got)
	}
	for _, e := range got {
		if e.Phase != devcontainer.ProvisionPhase {
			t.Errorf("event %q missing phase stamp", e.Step)
		}
	}
}

// TestEnsureCrewRuntime_Sink_NilIsNoop guards the OPTIONAL contract: callers
// that pass only {id, slug} (no sink) must behave exactly as before.
func TestEnsureCrewRuntime_Sink_NilIsNoop(t *testing.T) {
	p, capture := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:   "crew-nosink",
		Slug: "eng",
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime with nil sink must succeed: %v", err)
	}
	if capture.realCrew() == nil {
		t.Fatal("expected an agent-user container create even without a sink")
	}
}

// TestEnsureCrewRuntime_Sink_EmitsFailedOnError proves no failure is silent: an
// invalid crew id fails validation early and still emits provision.failed.
func TestEnsureCrewRuntime_Sink_EmitsFailedOnError(t *testing.T) {
	p, _ := newEnsureRuntimeFixture(t, Config{RuntimeImage: "fake/runtime:latest"})

	var got []devcontainer.ProvisionEvent
	sink := func(ev devcontainer.ProvisionEvent) { got = append(got, ev) }

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:            "../escape",
		Slug:          "eng",
		ProvisionSink: sink,
	})
	if err == nil {
		t.Fatal("expected validation error for unsafe crew id")
	}
	if !hasProvStep(got, devcontainer.ProvStepFailed, devcontainer.ProvStatusFailed) {
		t.Errorf("failed path must emit provision.failed: %+v", got)
	}
	if hasProvStep(got, devcontainer.ProvStepReady, "") {
		t.Errorf("failed path must not emit ready: %+v", got)
	}
}
