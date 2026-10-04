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
