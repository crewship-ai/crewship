package devcontainer

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const fixtureImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type identityCommitClient struct {
	mockCommitClient
	imageID string
}

func (c *identityCommitClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: c.imageID}}, nil
}

func TestProvisionCacheReturnsImmutableIdentity(t *testing.T) {
	cfg := &Config{Image: "alpine:3", ContainerEnv: map[string]string{"TEST": "identity"}}
	hash := configHash(cfg.Image, cfg, "", dockerfileGenFingerprint(cfg.Image, cfg))
	d := &identityCommitClient{
		mockCommitClient: mockCommitClient{existingImages: []string{cacheImageTag(hash)}},
		imageID:          strings.TrimPrefix(fixtureImageID, "sha256:"),
	}
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

type bareCommitClient struct{ mockCommitClient }

func (c *bareCommitClient) ContainerCommit(ctx context.Context, id string, options client.ContainerCommitOptions) (client.ContainerCommitResult, error) {
	result, err := c.mockCommitClient.ContainerCommit(ctx, id, options)
	result.ID = strings.TrimPrefix(result.ID, "sha256:")
	return result, err
}

func TestProvisionVerifiesBareCommitIdentity(t *testing.T) {
	cfg := &Config{Image: "alpine:3", ContainerEnv: map[string]string{"TEST": "identity"}}
	d := &bareCommitClient{}
	installer := NewInstaller(&mockDockerClient{exitCode: 0}, testLogger())
	p := NewProvisioner(d, installer, nil, testLogger())
	result, err := p.Provision(t.Context(), cfg.Image, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.CachedImage != fixtureImageID || d.inspectCalls == 0 {
		t.Fatalf("provisioned identity = %q; inspect calls = %d", result.CachedImage, d.inspectCalls)
	}
}
