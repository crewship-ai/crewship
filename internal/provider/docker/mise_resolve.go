package docker

import (
	"context"
	"fmt"
	"os"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// ResolveCrewMiseLock reuses the running provider's Docker client. The API
// supplies an image selected from the caller's own crew revision, never a
// request image. Missing/foreign ownership fails closed before builder work.
func (p *Provider) ResolveCrewMiseLock(ctx context.Context, imageID string, cfg *devcontainer.MiseConfig, bump bool) (*devcontainer.MiseResolution, error) {
	if p.client == nil || !dockerutil.IsLocalImageID(imageID) || devcontainer.ImageNamespace(p.cfg.InstanceID) == "" {
		return nil, fmt.Errorf("mise resolve: owned immutable image is required")
	}
	img, err := p.client.ImageInspect(ctx, imageID)
	if err != nil {
		return nil, fmt.Errorf("mise resolve: inspect selected image: %w", err)
	}
	if img.ID != imageID || img.Config == nil || img.Config.Labels[devcontainer.ImageInstanceLabel] != devcontainer.ImageNamespace(p.cfg.InstanceID) || img.Config.Labels[devcontainer.ImageKindLabel] != devcontainer.ImageKindCrew {
		return nil, fmt.Errorf("mise resolve: selected image is not owned by this installation")
	}
	return devcontainer.ResolveMiseLock(ctx, p.client, imageID, cfg, bump)
}

// ResolveMiseLock opens the same detected Docker endpoint as the provider,
// without initializing crew networks, volumes or runtime artifact mounts.
func ResolveMiseLock(ctx context.Context, imageID string, cfg *devcontainer.MiseConfig, bump bool) (*devcontainer.MiseResolution, error) {
	detected, err := Detect(ctx)
	if err != nil {
		return nil, err
	}
	opts := []client.Opt{client.WithHost(detected.Host)}
	if os.Getenv("DOCKER_HOST") != "" {
		opts = []client.Opt{client.FromEnv}
	}
	docker, err := client.New(opts...)
	if err != nil {
		return nil, err
	}
	defer docker.Close()
	return devcontainer.ResolveMiseLock(ctx, docker, imageID, cfg, bump)
}
