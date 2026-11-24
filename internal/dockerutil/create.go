package dockerutil

import (
	"context"
	"fmt"
	"strings"

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
	adj, err := rootlessPodmanOOMScoreAdj(ctx, cli, daemon.DaemonHost())
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

// IsPodman checks the components reported by the compatible daemon.
func IsPodman(ctx context.Context, daemon any) bool { ok, _ := podmanVersion(ctx, daemon); return ok }

func podmanVersion(ctx context.Context, daemon any) (bool, bool) {
	cli, ok := daemon.(interface {
		ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error)
	})
	if !ok {
		return false, false
	}
	version, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return false, false
	}
	for _, component := range version.Components {
		if strings.EqualFold(component.Name, "Podman Engine") {
			return true, true
		}
	}
	return false, len(version.Components) > 0
}
