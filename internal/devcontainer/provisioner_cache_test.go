package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
)

// TestDockerfileGenFingerprint_CoversPerFeatureRendering pins the fix for the
// stale-cache gap: the fingerprint renders a synthetic feature so the
// per-feature COPY/RUN/install-env template is part of the hash. If that
// template (or the synthetic-feature wiring) changes, this fails — a signal to
// confirm the cache will actually bust on per-feature rendering code changes.
func TestDockerfileGenFingerprint_CoversPerFeatureRendering(t *testing.T) {
	cfg := &Config{ContainerEnv: map[string]string{"FOO": "bar"}}
	fp := dockerfileGenFingerprint("ubuntu:24.04", cfg)
	if fp == "" {
		t.Fatal("fingerprint is empty — GenerateDockerfile failed on the probe feature")
	}
	for _, want := range []string{"# feature: fingerprint-probe", "install.sh", "PROBE_PATH"} {
		if !strings.Contains(fp, want) {
			t.Errorf("fingerprint missing %q — per-feature rendering is not covered:\n%s", want, fp)
		}
	}
}

// TestConfigHash_ChangesWithDockerfileFingerprint proves a generator/template
// change (modeled as a different fingerprint) yields a different cache hash, so
// a changed Dockerfile can never silently reuse a stale crewship-cache image.
func TestConfigHash_ChangesWithDockerfileFingerprint(t *testing.T) {
	cfg := &Config{ContainerEnv: map[string]string{"FOO": "bar"}}
	const base = "ubuntu:24.04"
	fp := dockerfileGenFingerprint(base, cfg)
	h1 := configHash(base, cfg, "", fp)
	h2 := configHash(base, cfg, "", fp+"\n# template changed")
	if h1 == h2 {
		t.Error("configHash did not change when the Dockerfile fingerprint changed — stale-cache risk")
	}
}

// offlineBaseImage is a base ref whose registry HEAD fails at once (nothing
// listens on port 1), so ensureImage keeps the mock's local copy instead of
// pulling — a pull would invalidate the image list on its own and mask what
// these tests measure. The existing Provision tests use ubuntu:22.04 and
// reach Docker Hub for the digest, which is fine for them but not here.
const offlineBaseImage = "127.0.0.1:1/crewship-test/base:1"

// TestImageExists_ConfirmsListHitAgainstDaemon pins the fix for #2431: the
// image list is memoised for 60 s, so an operator's `docker rmi` left a tag
// in the list that the daemon no longer had. Provision then reported a cache
// hit and "ready" without building, and the chat looped on a crew whose
// image nothing could start. A list hit is now confirmed with one inspect;
// a definitive not-found drops the memoised list and answers "absent".
func TestImageExists_ConfirmsListHitAgainstDaemon(t *testing.T) {
	const tag = "crewship-cache:abc123"
	cases := []struct {
		name           string
		prune          bool  // remove the tag from the daemon after the list is cached
		inspectErr     error // transport-level inspect failure (not a not-found)
		wantSecond     bool
		wantListCalls  int
		wantInspectMin int
		wantInspectMax int
	}{
		{
			name:           "present: one inspect per hit, the list is reused",
			wantSecond:     true,
			wantListCalls:  1,
			wantInspectMin: 2,
			wantInspectMax: 2,
		},
		{
			name:           "pruned after the list was cached: answers absent and drops the list",
			prune:          true,
			wantSecond:     false,
			wantListCalls:  2,
			wantInspectMin: 2,
			wantInspectMax: 2,
		},
		{
			name:           "inspect transport error: cannot tell, the list hit stands",
			inspectErr:     errors.New("daemon not responding"),
			wantSecond:     true,
			wantListCalls:  1,
			wantInspectMin: 2,
			wantInspectMax: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockCommitClient{existingImages: []string{tag}, inspectErr: tc.inspectErr}
			p := NewProvisioner(mock, nil, nil, testLogger())
			ctx := context.Background()

			first, err := p.imageExists(ctx, tag)
			if err != nil {
				t.Fatalf("first imageExists: %v", err)
			}
			if !first {
				t.Fatal("first imageExists = false, want true (tag is in the daemon)")
			}
			if tc.prune {
				mock.existingImages = nil // docker rmi outside Crewship
			}
			second, err := p.imageExists(ctx, tag)
			if err != nil {
				t.Fatalf("second imageExists: %v", err)
			}
			if second != tc.wantSecond {
				t.Errorf("second imageExists = %v, want %v", second, tc.wantSecond)
			}
			if mock.inspectCalls < tc.wantInspectMin || mock.inspectCalls > tc.wantInspectMax {
				t.Errorf("ImageInspect calls = %d, want %d..%d", mock.inspectCalls, tc.wantInspectMin, tc.wantInspectMax)
			}
			// A third look shows whether the memoised list survived: a
			// confirmed miss must have dropped it (relist), anything else
			// keeps serving from it.
			if _, err := p.imageExists(ctx, "crewship-cache:unrelated"); err != nil {
				t.Fatalf("third imageExists: %v", err)
			}
			if mock.imageListCalls != tc.wantListCalls {
				t.Errorf("ImageList calls = %d, want %d", mock.imageListCalls, tc.wantListCalls)
			}
		})
	}
}

// TestProvision_CacheHitPrunedImageRebuilds is the end-to-end shape of #2431
// inside the provisioner: the tag was served from the memoised list after
// the daemon lost it. The second Provision must build (create + commit under
// the same tag) rather than emit cache_hit → ready against nothing.
func TestProvision_CacheHitPrunedImageRebuilds(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	hash := configHash(offlineBaseImage, cfg, "", dockerfileGenFingerprint(offlineBaseImage, cfg))
	tag := cacheImageTag(hash)

	mock := &mockCommitClient{existingImages: []string{offlineBaseImage, tag}}
	inst := NewInstaller(&mockDockerClient{exitCode: 0}, testLogger())
	p := NewProvisioner(mock, inst, nil, testLogger())
	ctx := context.Background()

	var steps []string
	sink := func(ev ProvisionEvent) { steps = append(steps, ev.Step) }

	res, err := p.Provision(ctx, offlineBaseImage, cfg, "", WithProvisionSink(sink))
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	if res.CachedImage != tag || len(mock.createdContainers) != 0 {
		t.Fatalf("first Provision should be a pure cache hit: image=%q creates=%d", res.CachedImage, len(mock.createdContainers))
	}

	// docker rmi crewship-cache:<hash> while the list is still fresh.
	mock.existingImages = []string{offlineBaseImage}
	steps = nil

	res, err = p.Provision(ctx, offlineBaseImage, cfg, "", WithProvisionSink(sink))
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if res.CachedImage != tag {
		t.Errorf("second Provision image = %q, want %q", res.CachedImage, tag)
	}
	if len(mock.createdContainers) == 0 || len(mock.commitRefs) == 0 {
		t.Fatalf("second Provision did not rebuild: creates=%d commits=%d — the stale list was trusted", len(mock.createdContainers), len(mock.commitRefs))
	}
	if mock.commitRefs[len(mock.commitRefs)-1] != tag {
		t.Errorf("rebuild committed %q, want %q", mock.commitRefs[len(mock.commitRefs)-1], tag)
	}
	for _, s := range steps {
		if s == ProvStepCacheHit {
			t.Errorf("second Provision emitted %s for a tag the daemon no longer has", ProvStepCacheHit)
		}
	}
}

// TestProvision_ContainerNotFoundInvalidatesImageList: a temp-container
// create/start that fails because the daemon has no such image means the
// memoised list is wrong (the feature image it was told about is gone). Drop
// it so the next Provision relists and rebuilds instead of hitting the same
// stale entry; an unrelated create failure leaves the list alone.
func TestProvision_ContainerNotFoundInvalidatesImageList(t *testing.T) {
	notFound := fmt.Errorf("Error response from daemon: %w: No such image: crewship-feat:deadbeef", cerrdefs.ErrNotFound)
	cases := []struct {
		name          string
		createErr     error
		startErr      error
		wantListCalls int // ImageList calls after a follow-up IsCached
	}{
		{name: "create: no such image drops the list", createErr: notFound, wantListCalls: 2},
		{name: "create: untyped daemon text still counts", createErr: errors.New("No such image: crewship-feat:deadbeef"), wantListCalls: 2},
		{name: "start: no such object drops the list", startErr: errors.New("Error response from daemon: no such object: crewship-feat:deadbeef"), wantListCalls: 2},
		{name: "create: unrelated failure keeps the list", createErr: errors.New("OCI runtime create failed"), wantListCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
			mock := &mockCommitClient{existingImages: []string{offlineBaseImage}, createErr: tc.createErr, startErr: tc.startErr}
			p := NewProvisioner(mock, nil, nil, testLogger())
			ctx := context.Background()

			if _, err := p.Provision(ctx, offlineBaseImage, cfg, ""); err == nil {
				t.Fatal("Provision succeeded, want the container failure")
			}
			if mock.imageListCalls != 1 {
				t.Fatalf("ImageList calls during Provision = %d, want 1", mock.imageListCalls)
			}
			if _, err := p.IsCached(ctx, "other"); err != nil {
				t.Fatal(err)
			}
			if mock.imageListCalls != tc.wantListCalls {
				t.Errorf("ImageList calls after IsCached = %d, want %d", mock.imageListCalls, tc.wantListCalls)
			}
		})
	}
}
