package devcontainer

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestProvision_ExplicitRebuildBypassesExistingImage(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	hash := configHash(offlineBaseImage, cfg, "", dockerfileGenFingerprint(offlineBaseImage, cfg))
	tag := cacheImageTag(hash)
	mock := &mockCommitClient{existingImages: []string{offlineBaseImage, tag}}
	p := NewProvisioner(mock, NewInstaller(&mockDockerClient{exitCode: 0}, testLogger()), nil, testLogger())
	res, err := p.Provision(context.Background(), offlineBaseImage, cfg, "", WithForceRebuild(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.committedIDs) != 1 || res.CachedImage != tag {
		t.Fatalf("rebuild must commit a new image: commits=%v result=%+v", mock.committedIDs, res)
	}
}

func TestBuildersExplicitRebuildDisablesLayerCache(t *testing.T) {
	for _, kind := range []string{"docker", "apple"} {
		t.Run(kind, func(t *testing.T) {
			bin, record := recordingAppleCLI(t, 0)
			var b ImageBuilder
			if kind == "docker" {
				b = &DockerBuildKitBuilder{bin: bin, logger: slog.Default()}
			} else {
				b = &AppleContainerBuilder{bin: bin, logger: slog.Default()}
			}
			for _, force := range []bool{false, true} {
				if err := buildProvisionImage(context.Background(), b, t.TempDir(), "crewship-cache:rebuild-test", nil, force); err != nil {
					t.Fatal(err)
				}
				if got := strings.Contains(appleArgv(t, record), "--no-cache"); got != force {
					t.Fatalf("force=%v, no-cache=%v", force, got)
				}
			}
		})
	}
}

func TestExplicitRebuildRejectsUnsupportedBuilder(t *testing.T) {
	b := &recordingBuilder{available: true}
	err := buildProvisionImage(context.Background(), b, t.TempDir(), "crewship-cache:test", nil, true)
	if err == nil || b.calls != 0 {
		t.Fatalf("unsupported fresh build must fail before invoking cached builder: err=%v calls=%d", err, b.calls)
	}
}

func TestProvision_ExplicitRebuildRefreshesFeatureLayers(t *testing.T) {
	cacheDir := t.TempDir()
	ref := "ghcr.io/devcontainers/features/common-utils:2"
	covSeedFeature(t, cacheDir, ref, `{"id":"common-utils","version":"2"}`)
	mock := &mockCommitClient{}
	p := newCovProvisioner(mock, newCovExecClient(nil), cacheDir)
	b := &freshRecordingBuilder{probingBuilder: probingBuilder{recordingBuilder: recordingBuilder{available: true}}}
	p.SetImageBuilder(b)
	cfg := &Config{Image: offlineBaseImage, Features: map[string]map[string]any{ref: nil}}
	if _, err := p.Provision(context.Background(), offlineBaseImage, cfg, ""); err != nil {
		t.Fatal(err)
	}
	mock.existingImages = []string{offlineBaseImage, b.tag}
	p.invalidateImageListCache()
	if _, err := p.Provision(context.Background(), offlineBaseImage, cfg, "", WithForceRebuild(true)); err != nil {
		t.Fatal(err)
	}
	if b.calls != 2 || !b.noCache {
		t.Fatalf("explicit rebuild reused feature layers: calls=%d noCache=%v", b.calls, b.noCache)
	}
}
