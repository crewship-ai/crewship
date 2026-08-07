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

// indexOfProvStep returns the index of the first event matching step, or -1.
func indexOfProvStep(evs []devcontainer.ProvisionEvent, step string) int {
	for i, e := range evs {
		if e.Step == step {
			return i
		}
	}
	return -1
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

	if !hasProvStep(got, devcontainer.ProvStepContainerCreate, devcontainer.ProvStatusCompleted) {
		t.Errorf("missing container_create{completed} event: %+v", got)
	}
	if !hasProvStep(got, devcontainer.ProvStepReady, devcontainer.ProvStatusCompleted) {
		t.Errorf("missing ready{completed} event: %+v", got)
	}
	// Order matters: start → container_create → ready. Compare indexes in the
	// captured slice so a ready emitted before container_create fails the test
	// (presence-only checks would pass on a wrong order).
	startIdx := indexOfProvStep(got, devcontainer.ProvStepStart)
	createIdx := indexOfProvStep(got, devcontainer.ProvStepContainerCreate)
	readyIdx := indexOfProvStep(got, devcontainer.ProvStepReady)
	if startIdx < 0 {
		t.Errorf("missing provision.start event: %+v", got)
	}
	if !(startIdx < createIdx && createIdx < readyIdx) {
		t.Errorf("events out of order: start=%d container_create=%d ready=%d, want start < container_create < ready: %+v",
			startIdx, createIdx, readyIdx, got)
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

// TestEnsureCrewRuntime_Sink_CarriesImageProvenance is the wiring test for
// #1825: whatever ensureImage established about the image has to reach the
// sink, because the sink is what the journal is written from.
//
// The scenario is deliberately the discriminating one — a local copy WITH a
// manifest digest and a registry that cannot be reached to confirm it. Digest
// is therefore non-empty while the identity is UNverified, which is exactly
// the state a `Pinned: digest != ""` shortcut would misreport as pinned.
func TestEnsureCrewRuntime_Sink_CarriesImageProvenance(t *testing.T) {
	const localDigest = "sha256:cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333"

	p, _ := newEnsureRuntimeFixtureWithRepoDigests(t,
		Config{RuntimeImage: "fake/runtime:latest"},
		[]string{"fake/runtime@" + localDigest},
	)

	var got []devcontainer.ProvisionEvent
	sink := func(ev devcontainer.ProvisionEvent) { got = append(got, ev) }

	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:            "crew-prov",
		Slug:          "eng",
		ProvisionSink: sink,
	}); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	idx := indexOfProvStep(got, devcontainer.ProvStepImageResolved)
	if idx < 0 {
		t.Fatalf("missing image_resolved event — nothing records which image ran: %+v", got)
	}
	ev := got[idx]
	if ev.Digest != localDigest {
		t.Errorf("image_resolved Digest = %q, want %q", ev.Digest, localDigest)
	}
	if ev.Pinned {
		t.Error("Pinned must be false: the registry was never reached, so the digest is an unconfirmed local read-back")
	}
	if ev.Tag != "fake/runtime:latest" {
		t.Errorf("image_resolved Tag = %q, want the requested tag alongside the digest", ev.Tag)
	}

	// It must land BEFORE container_create — the point is to have recorded the
	// image even when the create then fails.
	if createIdx := indexOfProvStep(got, devcontainer.ProvStepContainerCreate); createIdx >= 0 && idx > createIdx {
		t.Errorf("image_resolved (%d) must precede container_create (%d)", idx, createIdx)
	}

	// The digest rides forward onto the terminal ready event too, so a consumer
	// that only watches for "ready" still learns what started.
	if readyIdx := indexOfProvStep(got, devcontainer.ProvStepReady); readyIdx >= 0 {
		if got[readyIdx].Digest != localDigest {
			t.Errorf("ready Digest = %q, want %q carried forward", got[readyIdx].Digest, localDigest)
		}
	}
}
