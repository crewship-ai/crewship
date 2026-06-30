package devcontainer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBuilder is an ImageBuilder test double: it records build invocations and
// never touches Docker.
type fakeBuilder struct {
	available bool
	builds    []string // tags built
	buildErr  error
}

func (f *fakeBuilder) Available() bool { return f.available }

func (f *fakeBuilder) Build(_ context.Context, _, tag string, onLog func(string)) error {
	f.builds = append(f.builds, tag)
	if onLog != nil {
		onLog("#1 [internal] load build definition")
	}
	return f.buildErr
}

func TestStageBuildContext_WritesDockerfileAndFeatures(t *testing.T) {
	t.Parallel()

	featDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(featDir, "install.sh"), []byte("#!/bin/sh\necho hi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(featDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featDir, "scripts", "helper.sh"), []byte("echo nested"), 0o644); err != nil {
		t.Fatal(err)
	}

	features := []*ResolvedFeature{
		{Ref: "ghcr.io/devcontainers/features/common-utils:2", Dir: featDir, Metadata: FeatureMetadata{ID: "common-utils"}},
	}
	ctxDir, dockerfile, tag, err := stageBuildContext("ubuntu:22.04", features, nil, nil)
	if err != nil {
		t.Fatalf("stageBuildContext: %v", err)
	}
	defer os.RemoveAll(ctxDir)

	// Dockerfile written to context root.
	if got, err := os.ReadFile(filepath.Join(ctxDir, "Dockerfile")); err != nil || string(got) != dockerfile {
		t.Fatalf("Dockerfile not written correctly: err=%v", err)
	}
	// Feature files staged (including nested dirs).
	if _, err := os.Stat(filepath.Join(ctxDir, "features", "common-utils", "install.sh")); err != nil {
		t.Errorf("install.sh not staged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ctxDir, "features", "common-utils", "scripts", "helper.sh")); err != nil {
		t.Errorf("nested file not staged: %v", err)
	}
	// Tag is deterministic and namespaced.
	if !strings.HasPrefix(tag, "crewship-feat:") {
		t.Errorf("unexpected tag %q", tag)
	}
	ctxDir2, _, tag2, err := stageBuildContext("ubuntu:22.04", features, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ctxDir2)
	if tag != tag2 {
		t.Errorf("tag not deterministic: %q vs %q", tag, tag2)
	}
}

// TestStageBuildContext_TagReflectsFeatureContent guards the cache key against
// silently reusing a stale image when a feature republishes new files under the
// same ref/options — i.e. when the generated Dockerfile is byte-identical but
// the feature's own files changed.
func TestStageBuildContext_TagReflectsFeatureContent(t *testing.T) {
	t.Parallel()

	mkFeature := func(body string) []*ResolvedFeature {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "install.sh"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return []*ResolvedFeature{
			{Ref: "ghcr.io/devcontainers/features/common-utils:2", Dir: d, Metadata: FeatureMetadata{ID: "common-utils"}},
		}
	}

	ctx1, df1, tag1, err := stageBuildContext("ubuntu:22.04", mkFeature("#!/bin/sh\necho v1"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ctx1)
	ctx2, df2, tag2, err := stageBuildContext("ubuntu:22.04", mkFeature("#!/bin/sh\necho v2"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ctx2)

	// Precondition: same base image, ref, and options → identical Dockerfile,
	// so the only thing that differs is the feature's file content.
	if df1 != df2 {
		t.Fatalf("precondition failed: Dockerfiles differ (%q vs %q)", df1, df2)
	}
	// The tag MUST change when feature content changes, or the exact-tag fast
	// path would silently reuse a stale image.
	if tag1 == tag2 {
		t.Errorf("tag must change when feature content changes; both = %q", tag1)
	}
}

func TestProvision_UsesBuildKitWhenAvailable(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)

	exec := newCovExecClient(nil)
	mock := &mockCommitClient{}
	p := newCovProvisioner(mock, exec, cacheDir)
	fb := &fakeBuilder{available: true}
	p.SetImageBuilder(fb)

	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{ref: nil}}
	res, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// BuildKit built exactly one feature image.
	if len(fb.builds) != 1 || !strings.HasPrefix(fb.builds[0], "crewship-feat:") {
		t.Fatalf("expected one feature-image build, got %v", fb.builds)
	}
	// Temp container created FROM the feature image, not the raw base.
	if len(mock.createdContainers) != 1 {
		t.Fatalf("expected 1 temp container, got %d", len(mock.createdContainers))
	}
	if got := mock.createdContainers[0].config.Image; got != fb.builds[0] {
		t.Errorf("temp container base = %q, want feature image %q", got, fb.builds[0])
	}
	// Features were baked by BuildKit → no install.sh ran in the container.
	for i := range exec.execs {
		if strings.Contains(exec.execCmd(i), "install.sh") {
			t.Errorf("install.sh should not run in-container on BuildKit path: %q", exec.execCmd(i))
		}
	}
	// Still produces the final cached image.
	if res.CachedImage == "" || len(mock.commitRefs) != 1 {
		t.Errorf("expected a committed cached image, got %q / %v", res.CachedImage, mock.commitRefs)
	}
}

func TestProvision_FallsBackWhenBuildKitUnavailable(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)

	exec := newCovExecClient(nil)
	mock := &mockCommitClient{}
	p := newCovProvisioner(mock, exec, cacheDir)
	p.SetImageBuilder(&fakeBuilder{available: false})

	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{ref: nil}}
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, ""); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Temp container created from the raw base image.
	if got := mock.createdContainers[0].config.Image; got != "ubuntu:22.04" {
		t.Errorf("temp container base = %q, want raw base", got)
	}
	// Features installed in-container → install.sh ran.
	ranInstall := false
	for i := range exec.execs {
		if strings.Contains(exec.execCmd(i), "install.sh") {
			ranInstall = true
		}
	}
	if !ranInstall {
		t.Error("expected install.sh to run in-container on fallback path")
	}
}

func TestProvision_BuildKitBuildErrorSurfaces(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)

	p := newCovProvisioner(&mockCommitClient{}, newCovExecClient(nil), cacheDir)
	p.SetImageBuilder(&fakeBuilder{available: true, buildErr: context.DeadlineExceeded})

	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{ref: nil}}
	if _, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, ""); err == nil {
		t.Fatal("expected build error to surface, got nil")
	}
}
