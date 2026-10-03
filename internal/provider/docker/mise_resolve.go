package docker

import (
	"context"
	"os"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

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
