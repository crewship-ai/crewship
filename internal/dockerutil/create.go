package dockerutil

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type containerCreator interface {
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
}

// CreateContainer carries a local rootless Podman service's OOM adjustment into
// the OCI spec so crun can apply it on exec as well as on start.
func CreateContainer(ctx context.Context, cli containerCreator, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	daemon, ok := cli.(interface{ DaemonHost() string })
	if !ok || (opts.HostConfig != nil && opts.HostConfig.OomScoreAdj != 0) {
		return cli.ContainerCreate(ctx, opts)
	}
	adj, err := rootlessPodmanOOMScoreAdj(ctx, daemon.DaemonHost())
	if err != nil {
		return client.ContainerCreateResult{}, fmt.Errorf("detect rootless Podman OOM adjustment: %w", err)
	}
	if adj != 0 {
		host := container.HostConfig{}
		if opts.HostConfig != nil {
			host = *opts.HostConfig
		}
		host.OomScoreAdj = adj
		opts.HostConfig = &host
	}
	return cli.ContainerCreate(ctx, opts)
}
