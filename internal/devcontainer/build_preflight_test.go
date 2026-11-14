package devcontainer

import (
	"context"
	"errors"
	"testing"
)

var errNoRoom = errors.New("no room")

// A refused build must stop before anything is written: no temp container,
// no commit, and a provision.failed event that names the preflight.
func TestProvision_BuildPreflightRefusesBeforeAnyWrite(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	daemon := &mockCommitClient{existingImages: []string{offlineBaseImage}}
	p := NewProvisioner(daemon, NewInstaller(&mockDockerClient{exitCode: 0}, testLogger()), nil, testLogger())
	calls := 0
	p.SetBuildPreflight(func(context.Context) error { calls++; return errNoRoom })

	var failedDetail string
	sink := func(ev ProvisionEvent) {
		if ev.Step == ProvStepFailed {
			failedDetail = ev.Detail
		}
	}
	_, err := p.Provision(context.Background(), offlineBaseImage, cfg, "", WithProvisionSink(sink))
	if !errors.Is(err, errNoRoom) {
		t.Fatalf("Provision error = %v, want the preflight's error", err)
	}
	if calls != 1 {
		t.Errorf("preflight calls = %d, want 1", calls)
	}
	if len(daemon.createdContainers) != 0 || len(daemon.commitRefs) != 0 {
		t.Errorf("a refused build wrote to the daemon: creates=%d commits=%v", len(daemon.createdContainers), daemon.commitRefs)
	}
	if failedDetail != ProvStepStoragePreflight {
		t.Errorf("provision.failed detail = %q, want %q", failedDetail, ProvStepStoragePreflight)
	}
}

// Reusing an image writes nothing, so low disk space must not refuse it.
func TestProvision_BuildPreflightIsNotConsultedOnCacheHit(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}
	hash := configHash("ubuntu:22.04", cfg, "", dockerfileGenFingerprint("ubuntu:22.04", cfg))
	p := NewProvisioner(&mockCommitClient{existingImages: []string{cacheImageTag(hash)}}, nil, nil, testLogger())
	p.SetBuildPreflight(func(context.Context) error {
		t.Error("preflight consulted on a cache hit")
		return errNoRoom
	})
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, ""); err != nil {
		t.Fatalf("cache hit refused: %v", err)
	}
}

// A configuration that needs no build selects no image and writes nothing.
func TestProvision_BuildPreflightIsNotConsultedWithoutCustomizations(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage}
	p := NewProvisioner(&mockCommitClient{existingImages: []string{offlineBaseImage}}, nil, nil, testLogger())
	p.SetBuildPreflight(func(context.Context) error {
		t.Error("preflight consulted although nothing is built")
		return errNoRoom
	})
	if _, err := p.Provision(context.Background(), offlineBaseImage, cfg, ""); err != nil {
		t.Fatalf("no-build provision refused: %v", err)
	}
}

func TestProvision_BuildPreflightPassingLetsTheBuildRun(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	daemon := &mockCommitClient{existingImages: []string{offlineBaseImage}}
	p := NewProvisioner(daemon, NewInstaller(&mockDockerClient{exitCode: 0}, testLogger()), nil, testLogger())
	p.SetBuildPreflight(func(context.Context) error { return nil })
	if _, err := p.Provision(context.Background(), offlineBaseImage, cfg, ""); err != nil {
		t.Fatal(err)
	}
	if len(daemon.commitRefs) != 1 {
		t.Errorf("commits = %v, want one", daemon.commitRefs)
	}
}
