package docker

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// NewCleanupRuntime deliberately does not construct a full Provider: detection
// and a REST client are read-only, unlike runtime boot's network/secret sweeps.
// Called on every scan, so a daemon absent at boot may reconnect later.
func NewCleanupRuntime(ctx context.Context) (resourcelifecycle.Runtime, error) {
	detected, err := Detect(ctx)
	if err != nil {
		return nil, err
	}
	cli, err := client.New(client.FromEnv, client.WithHost(detected.Host))
	if err != nil {
		return nil, err
	}
	return &cleanupRuntime{client: cli}, nil
}

type cleanupRuntime struct{ client *client.Client }

func (r *cleanupRuntime) Close() error { return r.client.Close() }
func (r *cleanupRuntime) List(ctx context.Context) ([]resourcelifecycle.Container, error) {
	result, err := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]resourcelifecycle.Container, 0, len(result.Items))
	for _, c := range result.Items {
		out = append(out, resourcelifecycle.Container{ID: c.ID, InstanceID: c.Labels[resourcelifecycle.InstanceLabel], CrewID: c.Labels[crewCrewIDLabel], Kind: c.Labels[crewKindLabel]})
	}
	return out, nil
}
func cleanupError(err error) error {
	if cerrdefs.IsNotFound(err) {
		return resourcelifecycle.ErrNotFound
	}
	return err
}
func (r *cleanupRuntime) Inspect(ctx context.Context, id string) (resourcelifecycle.Container, error) {
	result, err := r.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return resourcelifecycle.Container{}, cleanupError(err)
	}
	c := result.Container
	if c.Config == nil || c.ID == "" {
		return resourcelifecycle.Container{}, fmt.Errorf("incomplete container inspection")
	}
	out := resourcelifecycle.Container{ID: c.ID, InstanceID: c.Config.Labels[resourcelifecycle.InstanceLabel], CrewID: c.Config.Labels[crewCrewIDLabel], Kind: c.Config.Labels[crewKindLabel]}
	for _, m := range c.Mounts {
		out.Mounts = append(out.Mounts, resourcelifecycle.Mount{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination})
	}
	return out, nil
}
func (r *cleanupRuntime) Stop(ctx context.Context, id string) error {
	timeout := 10
	_, err := r.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return cleanupError(err)
}
func (r *cleanupRuntime) Remove(ctx context.Context, id string) error {
	// Do not route through forceTeardown or sidecar teardown: both delete volumes.
	_, err := r.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{RemoveVolumes: false, Force: false})
	return cleanupError(err)
}

// ListNetworks reports per-crew networks (#2240) with their ownership labels.
// Every network on the daemon is listed; the controller selects by labels,
// never by name.
func (r *cleanupRuntime) ListNetworks(ctx context.Context) ([]resourcelifecycle.Network, error) {
	result, err := r.client.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	out := []resourcelifecycle.Network{}
	for _, n := range result.Items {
		if n.Labels[crewKindLabel] != resourcelifecycle.NetworkKind {
			continue
		}
		out = append(out, resourcelifecycle.Network{ID: n.ID, InstanceID: n.Labels[resourcelifecycle.InstanceLabel], CrewID: n.Labels[crewCrewIDLabel], Kind: n.Labels[crewKindLabel]})
	}
	return out, nil
}

// RemoveNetwork removes one network. A network with containers still attached
// is reported as ErrNetworkInUse; there is no force.
func (r *cleanupRuntime) RemoveNetwork(ctx context.Context, id string) error {
	_, err := r.client.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	if err == nil {
		return nil
	}
	if cerrdefs.IsNotFound(err) {
		return resourcelifecycle.ErrNotFound
	}
	if cerrdefs.IsConflict(err) || cerrdefs.IsPermissionDenied(err) || strings.Contains(strings.ToLower(err.Error()), "active endpoints") {
		return resourcelifecycle.ErrNetworkInUse
	}
	return err
}

var _ resourcelifecycle.NetworkRuntime = (*cleanupRuntime)(nil)

// SetCrewOwnerCheck is startup wiring, before any provider work is dispatched.
func (p *Provider) SetCrewOwnerCheck(check func(context.Context, string) error) {
	p.cfg.OwnerActive = check
}
