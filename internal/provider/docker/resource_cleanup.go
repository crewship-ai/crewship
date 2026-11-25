package docker

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
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

// OrphanRetentionReason qualifies a rowless crew independently of the
// positive-tombstone teardown. In particular, stopped application containers
// and a restored database's only attachment to persistent data stay intact.
func (r *cleanupRuntime) OrphanRetentionReason(ctx context.Context, expected resourcelifecycle.Container) (string, error) {
	result, err := r.client.ContainerInspect(ctx, expected.ID, client.ContainerInspectOptions{Size: true})
	if err != nil {
		return "", err
	}
	c := result.Container
	reason := rowlessContainerRetention(c, expected)
	if reason != "" {
		return reason, nil
	}
	// Even an empty container might be the last reference to an image that
	// the engine no longer lists (e.g. an old containerd export).
	img, err := r.client.ImageInspect(ctx, c.Image)
	if cerrdefs.IsNotFound(err) {
		return "missing-image", nil
	}
	if err != nil {
		return "", err
	}
	if img.ID == "" || dockerutil.ConfigImageID(img.ID) != dockerutil.ConfigImageID(c.Image) {
		return "unverified-image", nil
	}
	// Metadata blocks count towards SizeRw on the containerd snapshotter.
	// Conversely, a zero-byte file is still a change. Ask for the actual diff.
	diff, err := r.client.ContainerDiff(ctx, c.ID, client.ContainerDiffOptions{})
	if err != nil {
		return "", err
	}
	if len(diff.Changes) != 0 {
		return "writable-layer", nil
	}
	if diff.Changes == nil {
		// Docker and Podman encode a successful zero-entry diff as JSON null.
		// Empty HTTP bodies and truncated JSON surface as SDK decode errors.
		if c.SizeRw == nil || *c.SizeRw < 0 {
			return "unverified-writable-layer", nil
		}
		version, err := r.client.ServerVersion(ctx, client.ServerVersionOptions{})
		if err != nil {
			return "", err
		}
		for _, component := range version.Components {
			if component.Name == "Engine" || component.Name == "Podman Engine" {
				return "", nil
			}
		}
		return "unverified-writable-layer", nil
	}
	return "", nil
}

func rowlessContainerRetention(c container.InspectResponse, expected resourcelifecycle.Container) string {
	if len(c.ID) != 64 || dockerutil.ConfigImageID(c.ID) == "" || c.ID != expected.ID || c.Config == nil || c.HostConfig == nil || c.State == nil || c.NetworkSettings == nil || dockerutil.ConfigImageID(c.Image) == "" {
		return "incomplete-inspection"
	}
	labels := c.Config.Labels
	if strings.TrimSpace(expected.InstanceID) == "" || strings.TrimSpace(expected.CrewID) == "" || (expected.Kind != "crew" && expected.Kind != "sidecar") || labels[resourcelifecycle.InstanceLabel] != expected.InstanceID || labels[crewCrewIDLabel] != expected.CrewID || labels[crewKindLabel] != expected.Kind {
		return "ownership-changed"
	}
	st, h := c.State, c.HostConfig
	if st.Status != "created" || st.Running || st.Paused || st.Restarting || st.Dead || st.Pid != 0 || c.RestartCount != 0 || !h.RestartPolicy.IsNone() {
		return "runtime-used"
	}
	if !h.ReadonlyRootfs {
		return "writable-layer"
	}
	if h.PublishAllPorts || len(h.PortBindings) > 0 || len(c.NetworkSettings.Ports) > 0 {
		return "published-port"
	}
	if !h.NetworkMode.IsNone() || len(h.Links) > 0 {
		return "network-attached"
	}
	for name := range c.NetworkSettings.Networks {
		if name != "none" {
			return "network-attached"
		}
	}
	if len(c.Mounts) > 0 || len(h.Binds) > 0 || len(h.Mounts) > 0 || len(h.VolumesFrom) > 0 || len(c.Config.Volumes) > 0 {
		return "data-attachment"
	}
	if expected.Kind == "sidecar" || labels[sidecarSvcLabel] != "" {
		return "application"
	}
	for key := range labels {
		if strings.HasPrefix(key, "com.docker.compose.") || strings.HasPrefix(key, "io.podman.compose.") || strings.HasPrefix(key, "io.kubernetes.") {
			return "application"
		}
	}
	return ""
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
// The daemon filters on the kind label; the controller still selects by the
// instance and crew labels, never by name.
func (r *cleanupRuntime) ListNetworks(ctx context.Context) ([]resourcelifecycle.Network, error) {
	result, err := r.client.NetworkList(ctx, client.NetworkListOptions{
		Filters: make(client.Filters).Add("label", crewKindLabel+"="+resourcelifecycle.NetworkKind),
	})
	if err != nil {
		return nil, err
	}
	out := []resourcelifecycle.Network{}
	for _, n := range result.Items {
		// The daemon filtered on the kind label; re-check rather than trust
		// a proxy or an older daemon to have applied the filter.
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
	// Docker answers 403 for "has active endpoints", but so does a socket
	// proxy without NetworkRemove in its allowlist. Only the endpoints
	// message (or a conflict) is in-use; any other refusal is surfaced.
	if cerrdefs.IsConflict(err) || strings.Contains(strings.ToLower(err.Error()), "active endpoints") {
		return resourcelifecycle.ErrNetworkInUse
	}
	if cerrdefs.IsPermissionDenied(err) {
		return fmt.Errorf("%w: %v", resourcelifecycle.ErrNetworkForbidden, err)
	}
	return err
}

var _ resourcelifecycle.NetworkRuntime = (*cleanupRuntime)(nil)

// SetCrewOwnerCheck is startup wiring, before any provider work is dispatched.
func (p *Provider) SetCrewOwnerCheck(check func(context.Context, string) error) {
	p.cfg.OwnerActive = check
}
