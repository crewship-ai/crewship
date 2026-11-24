package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type containerCreator interface {
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
}

// CreateCrewContainer uses an authenticated connection for Crewship's shared
// SELinux binds. The source checks and the create request use the same socket,
// including when the HTTP transport redials.
func CreateCrewContainer(ctx context.Context, cli *client.Client, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if opts.HostConfig == nil || !slices.ContainsFunc(opts.HostConfig.Binds, sharedCrewRelabel) {
		return CreateContainer(ctx, cli, opts)
	}
	host := cli.DaemonHost()
	socket, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return client.ContainerCreateResult{}, fmt.Errorf("SELinux crew bind relabel requires a verified local Podman service")
	}
	bound, err := client.New(client.WithHost(host), client.WithAPIVersion(cli.ClientVersion()),
		client.WithDialContext(func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialPodmanBinds(ctx, socket, opts.HostConfig.Binds)
		}))
	if err != nil {
		return client.ContainerCreateResult{}, err
	}
	defer bound.Close()
	created, err := CreateContainer(ctx, bound, opts)
	var refused *relabelRefusal
	if errors.As(err, &refused) {
		return created, fmt.Errorf("verify Podman SELinux binds: %w", refused.cause)
	}
	return created, err
}

// Keep filesystem errors opaque to the Moby dial transport, which treats
// ENOENT and EACCES as failures to reach the socket. Restore the cause at
// the CreateCrewContainer boundary.
type relabelRefusal struct{ cause error }

func (e *relabelRefusal) Error() string { return e.cause.Error() }

func sharedCrewRelabel(bind string) bool {
	return strings.HasSuffix(bind, ":ro,z") || strings.HasSuffix(bind, ":rw,z")
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
