package devcontainer

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const fixtureImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type identityCommitClient struct{ mockCommitClient }

func (c *identityCommitClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: fixtureImageID}}, nil
}

func TestProvisionCacheReturnsImmutableIdentity(t *testing.T) {
	cfg := &Config{Image: "alpine:3", ContainerEnv: map[string]string{"TEST": "identity"}}
	hash := configHash(cfg.Image, cfg, "", dockerfileGenFingerprint(cfg.Image, cfg))
	d := &identityCommitClient{mockCommitClient{existingImages: []string{cacheImageTag(hash)}}}
	p := NewProvisioner(d, nil, nil, testLogger())
	result, err := p.Provision(context.Background(), cfg.Image, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.CachedImage != fixtureImageID {
		t.Fatalf("published movable reference %q, want %s", result.CachedImage, fixtureImageID)
	}
	if !strings.HasPrefix(result.CachedImage, "sha256:") {
		t.Fatal("published reference is not local image identity")
	}
}

type podmanCommitMock struct {
	identityCommitClient
	commitID  string
	inspected string
	committed bool
}

func (m *podmanCommitMock) ContainerCommit(context.Context, string, client.ContainerCommitOptions) (client.ContainerCommitResult, error) {
	m.committed = true
	return client.ContainerCommitResult{ID: m.commitID}, nil
}

func (m *podmanCommitMock) ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	if !m.committed {
		return m.identityCommitClient.ImageInspect(ctx, ref, opts...)
	}
	m.inspected = ref
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: strings.TrimPrefix(fixtureImageID, "sha256:")}}, nil
}

func TestPodmanCommitIdentity(t *testing.T) {
	bare := strings.TrimPrefix(fixtureImageID, "sha256:")
	for _, tc := range []struct {
		name, id string
		ok       bool
	}{
		{"bare", bare, true}, {"canonical", fixtureImageID, true},
		{"short", bare[:12], false}, {"uppercase", strings.ToUpper(bare), false},
		{"other config or manifest digest", "sha256:" + strings.Repeat("b", 64), false},
		{"registry reference", "example/image@" + fixtureImageID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &podmanCommitMock{commitID: tc.id}
			cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"TEST": "identity"}}
			inst := NewInstaller(&mockDockerClient{}, testLogger())
			result, err := NewProvisioner(m, inst, nil, testLogger()).Provision(context.Background(), cfg.Image, cfg, "")
			if tc.ok && m.inspected != fixtureImageID {
				t.Fatalf("inspected %q, want immutable config ID", m.inspected)
			}
			if tc.ok && (err != nil || result.CachedImage != fixtureImageID) {
				t.Fatalf("wanted verified config ID, got %+v, %v", result, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("accepted %q", tc.id)
			}
		})
	}
}
