package devcontainer

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// stepStatus is a compact (step,status) projection of a ProvisionEvent used to
// assert the ordered audit trail without coupling to durations/details.
type stepStatus struct {
	step   string
	status string
}

func collectEvents() (ProvisionSink, *[]ProvisionEvent) {
	var got []ProvisionEvent
	sink := func(ev ProvisionEvent) { got = append(got, ev) }
	return sink, &got
}

func steps(evs []ProvisionEvent) []stepStatus {
	out := make([]stepStatus, 0, len(evs))
	for _, e := range evs {
		out = append(out, stepStatus{e.Step, e.Status})
	}
	return out
}

func hasStep(evs []ProvisionEvent, step, status string) bool {
	for _, e := range evs {
		if e.Step == step && (status == "" || e.Status == status) {
			return true
		}
	}
	return false
}

func indexOfStep(evs []ProvisionEvent, step string) int {
	for i, e := range evs {
		if e.Step == step {
			return i
		}
	}
	return -1
}

// TestProvision_Sink_BuildKitOrderedEvents verifies the sink receives the full,
// ordered audit trail for a BuildKit build: start → resolve → build start →
// per-feature → build done → container create → env apply → ready. Every event
// carries the canonical phase, and durations land on the timed steps.
func TestProvision_Sink_BuildKitOrderedEvents(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)

	exec := newCovExecClient(nil)
	mock := &mockCommitClient{}
	p := newCovProvisioner(mock, exec, cacheDir)
	p.SetImageBuilder(&fakeBuilder{available: true})

	cfg := &Config{
		Image:        "ubuntu:22.04",
		Features:     map[string]map[string]any{ref: nil},
		ContainerEnv: map[string]string{"FOO": "bar"},
	}

	sink, got := collectEvents()
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "", WithProvisionSink(sink)); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	want := []stepStatus{
		{ProvStepStart, ""},
		{ProvStepResolveFeatures, ProvStatusCompleted},
		{ProvStepImageBuildStart, ProvStatusStarted},
		{ProvStepFeatureInstall, ProvStatusStarted},
		{ProvStepFeatureInstall, ProvStatusCompleted},
		{ProvStepImageBuildDone, ProvStatusCompleted},
		{ProvStepContainerCreate, ProvStatusCompleted},
		{ProvStepContainerEnvApply, ProvStatusStarted},
		{ProvStepContainerEnvApply, ProvStatusCompleted},
		{ProvStepReady, ProvStatusCompleted},
	}
	gotSteps := steps(*got)
	if len(gotSteps) != len(want) {
		t.Fatalf("event count = %d, want %d:\n%#v", len(gotSteps), len(want), gotSteps)
	}
	for i := range want {
		if gotSteps[i] != want[i] {
			t.Errorf("event[%d] = %+v, want %+v\nfull: %#v", i, gotSteps[i], want[i], gotSteps)
		}
	}

	// Every event is phase-stamped; the per-feature events name the feature.
	for _, e := range *got {
		if e.Phase != ProvisionPhase {
			t.Errorf("event %q missing phase stamp", e.Step)
		}
	}
	for _, e := range *got {
		if e.Step == ProvStepFeatureInstall && e.Feature != "common-utils" {
			t.Errorf("feature_install event has Feature=%q, want common-utils", e.Feature)
		}
	}
	// resolve_features and image_build_done carry a duration.
	if e := (*got)[indexOfStep(*got, ProvStepImageBuildDone)]; e.DurationMs < 0 {
		t.Errorf("image_build_done missing duration")
	}
	if e := (*got)[indexOfStep(*got, ProvStepImageBuildStart)]; e.Tag == "" {
		t.Errorf("image_build_start must carry the feature image tag")
	}
}

// TestProvision_Sink_FallbackPerFeatureEvents verifies the container-commit
// fallback path (no BuildKit) emits true per-feature install events and no
// image_build_* events.
func TestProvision_Sink_FallbackPerFeatureEvents(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, cacheDir)
	p.SetImageBuilder(&fakeBuilder{available: false})

	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{ref: nil}}
	sink, got := collectEvents()
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "", WithProvisionSink(sink)); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if !hasStep(*got, ProvStepFeatureInstall, ProvStatusStarted) ||
		!hasStep(*got, ProvStepFeatureInstall, ProvStatusCompleted) {
		t.Errorf("fallback path must emit per-feature started+completed:\n%#v", steps(*got))
	}
	if hasStep(*got, ProvStepImageBuildStart, "") || hasStep(*got, ProvStepImageBuildDone, "") {
		t.Errorf("fallback path must NOT emit image_build_* events:\n%#v", steps(*got))
	}
	if !hasStep(*got, ProvStepReady, ProvStatusCompleted) {
		t.Errorf("fallback path must reach ready:\n%#v", steps(*got))
	}
}

// TestProvision_Sink_BuildFailurePropagates proves a BuildKit build failure
// emits provision.failed (with the failing step in Detail) AND returns the
// error — nothing fails silently.
func TestProvision_Sink_BuildFailurePropagates(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)

	p := newCovProvisioner(&mockCommitClient{}, newCovExecClient(nil), cacheDir)
	p.SetImageBuilder(&fakeBuilder{available: true, buildErr: context.DeadlineExceeded})

	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{ref: nil}}
	sink, got := collectEvents()
	_, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "", WithProvisionSink(sink))
	if err == nil {
		t.Fatal("expected build error to propagate, got nil")
	}

	var failed *ProvisionEvent
	for i := range *got {
		if (*got)[i].Step == ProvStepFailed {
			failed = &(*got)[i]
		}
	}
	if failed == nil {
		t.Fatalf("expected a provision.failed event, got:\n%#v", steps(*got))
	}
	if failed.Status != ProvStatusFailed {
		t.Errorf("failed event status = %q, want %q", failed.Status, ProvStatusFailed)
	}
	if failed.Detail != ProvStepImageBuildStart {
		t.Errorf("failed event detail = %q, want %q", failed.Detail, ProvStepImageBuildStart)
	}
	if failed.Error == "" {
		t.Error("failed event must carry the underlying error")
	}
	// Must NOT have reached ready.
	if hasStep(*got, ProvStepReady, "") {
		t.Errorf("failed build must not emit ready:\n%#v", steps(*got))
	}
}

// TestProvision_Sink_FeatureInstallFailure proves the container-commit path
// emits a per-feature feature_install{status:failed} and propagates the error.
func TestProvision_Sink_FeatureInstallFailure(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/t/features/broken:1"
	covSeedFeature(t, cacheDir, ref, `{"id":"broken","version":"1"}`)

	exec := newCovExecClient(func(_ int, cfg container.ExecOptions) covExecResult {
		if strings.Contains(strings.Join(cfg.Cmd, " "), "install.sh") {
			return covExecResult{output: "compile error", exitCode: 1}
		}
		return covExecResult{}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, cacheDir)
	p.SetImageBuilder(&fakeBuilder{available: false})

	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{ref: nil}}
	sink, got := collectEvents()
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "", WithProvisionSink(sink)); err == nil {
		t.Fatal("expected feature install error to propagate, got nil")
	}

	var failed *ProvisionEvent
	for i := range *got {
		e := (*got)[i]
		if e.Step == ProvStepFeatureInstall && e.Status == ProvStatusFailed {
			failed = &(*got)[i]
		}
	}
	if failed == nil {
		t.Fatalf("expected feature_install{failed}, got:\n%#v", steps(*got))
	}
	if failed.Feature != "broken" || failed.Error == "" {
		t.Errorf("feature_install failure = %+v, want feature=broken with error", *failed)
	}
}

// TestProvision_Sink_CacheHit verifies the no-build fast path is still audited:
// start → cache_hit → ready, and nothing else.
func TestProvision_Sink_CacheHit(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}
	hash := configHash("ubuntu:22.04", cfg, "")
	tag := cacheImageTag(hash)

	mock := &mockCommitClient{existingImages: []string{tag}}
	p := NewProvisioner(mock, nil, nil, testLogger())

	sink, got := collectEvents()
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "", WithProvisionSink(sink)); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	want := []stepStatus{
		{ProvStepStart, ""},
		{ProvStepCacheHit, ProvStatusCompleted},
		{ProvStepReady, ProvStatusCompleted},
	}
	gotSteps := steps(*got)
	if len(gotSteps) != len(want) {
		t.Fatalf("cache-hit events = %#v, want %#v", gotSteps, want)
	}
	for i := range want {
		if gotSteps[i] != want[i] {
			t.Errorf("event[%d] = %+v, want %+v", i, gotSteps[i], want[i])
		}
	}
	// cache_hit and ready must carry the tag for audit correlation.
	if e := (*got)[1]; e.Tag != tag {
		t.Errorf("cache_hit tag = %q, want %q", e.Tag, tag)
	}
}

// TestProvision_Sink_NilIsNoop guards the OPTIONAL contract: a provision with no
// sink behaves exactly as before (no panic, succeeds).
func TestProvision_Sink_NilIsNoop(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04", ContainerEnv: map[string]string{"FOO": "bar"}}
	dockerMock := &mockDockerClient{exitCode: 0}
	inst := NewInstaller(dockerMock, testLogger())
	p := NewProvisioner(&mockCommitClient{}, inst, nil, testLogger())

	// No WithProvisionSink → onProvision is nil → emitProvision is a no-op.
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, ""); err != nil {
		t.Fatalf("nil-sink Provision must succeed: %v", err)
	}
}
