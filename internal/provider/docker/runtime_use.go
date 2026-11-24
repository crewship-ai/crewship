package docker

import (
	"context"
	"errors"
	"fmt"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

var _ provider.CrewRuntimeUseProvider = (*Provider)(nil)
var _ provider.CrewIdleStopper = (*Provider)(nil)

type managedRuntimeUseKey struct{}

func (p *Provider) runtimeUseGate(crewID string) *provider.RuntimeUseGate {
	value, _ := p.runtimeUses.LoadOrStore(crewID, &provider.RuntimeUseGate{})
	return value.(*provider.RuntimeUseGate)
}

// SetRuntimeIdleVerifier installs controller evidence for recovered runs and
// external occupancy. With no verifier automatic activation is refused.
func (p *Provider) SetRuntimeIdleVerifier(fn func(context.Context, string, string) error) {
	p.runtimeIdleMu.Lock()
	p.runtimeIdleVerifier = fn
	p.runtimeIdleMu.Unlock()
}

func (p *Provider) AcquireCrewRuntimeUse(ctx context.Context, cfg provider.CrewConfig) (*provider.RuntimeUse, error) {
	if cfg.ID == "" {
		return nil, errors.New("runtime use requires a crew id")
	}
	ctx = context.WithValue(ctx, managedRuntimeUseKey{}, true)
	gate := p.runtimeUseGate(cfg.ID)
	emitUpgrade := func(step string) {
		if cfg.ProvisionSink != nil {
			cfg.ProvisionSink(devcontainer.ProvisionEvent{Phase: devcontainer.ProvisionPhase, Step: step, Status: devcontainer.ProvStatusStarted})
		}
	}
	activations := 0
	for {
		waited := false
		release, err := gate.UseNotify(ctx, func() { waited = true; emitUpgrade(devcontainer.ProvStepRuntimeDrain) })
		if waited && err != nil {
			return nil, fmt.Errorf("%w: waiting for runtime replacement: %w", provider.ErrRuntimeImageUpdatePending, err)
		}
		if err != nil {
			return nil, err
		}
		id, err := p.EnsureCrewRuntime(ctx, cfg)
		if err == nil {
			return provider.NewRuntimeUse(cfg.ID, id, release), nil
		}
		release()
		var refresh *sidecarRefreshPendingError
		if errors.As(err, &refresh) {
			// Never waits: a runtime in use keeps its outdated sidecar.
			ctx = p.recycleStaleSidecarRuntime(ctx, cfg, refresh)
			continue
		}
		var pending *provider.RuntimeImageUpdatePendingError
		if !errors.As(err, &pending) {
			return nil, err
		}
		if provider.RuntimeUseNoWait(ctx) {
			return nil, err
		}
		activations++
		if activations > 3 {
			return nil, fmt.Errorf("%w: runtime did not converge after three activations", provider.ErrRuntimeImageUpdatePending)
		}
		unlock, immediate := gate.TryExclusive()
		if !immediate {
			emitUpgrade(devcontainer.ProvStepRuntimeDrain)
			unlock, err = gate.Exclusive(ctx)
			if err != nil {
				return nil, fmt.Errorf("%w: waiting for runtime users: %w", provider.ErrRuntimeImageUpdatePending, err)
			}
		}
		emitUpgrade(devcontainer.ProvStepRuntimeUpgrade)
		err = p.activateUnusedCrewRuntime(ctx, cfg)
		unlock()
		if err != nil {
			return nil, err
		}
		// Reacquire shared use and resolve again. Another desired revision may
		// have won before this reservation; never return a stale runtime handle.
	}
}

// removeForReconcile makes every automatic recreation path obey managed
// admission for healthy containers, including mount migration. Restart backoff
// retains its separate crash recovery path. Only the exclusive
// activation path may stop a confirmed idle runtime; reconciliation itself
// removes only freshly inspected inactive containers, without Docker force.
func (p *Provider) removeForReconcile(ctx context.Context, containerID, crewID string) error {
	if managed, _ := ctx.Value(managedRuntimeUseKey{}).(bool); !managed {
		return p.forceTeardown(ctx, containerID, crewID)
	}
	inspected, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	c := inspected.Container
	state := c.State
	if state == nil || state.Running || state.Paused || state.Restarting || (state.Status != "exited" && state.Status != "created") {
		return &provider.RuntimeImageUpdatePendingError{ContainerID: containerID, CurrentImageID: c.Image}
	}
	if err := p.verifyReplacementVolumes(ctx, c.Mounts, crewID, crewSlugFromInspect(c.Config)); err != nil {
		return err
	}
	if _, err := p.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: false, RemoveVolumes: true}); err != nil {
		return fmt.Errorf("remove inactive runtime for reconciliation: %w", err)
	}
	p.evictWarm(crewID)
	p.forgetFenced(containerID)
	return nil
}

func (p *Provider) activateUnusedCrewRuntime(ctx context.Context, cfg provider.CrewConfig) error {
	_, err := p.EnsureCrewRuntime(ctx, cfg)
	if err == nil {
		return nil
	}
	var pending *provider.RuntimeImageUpdatePendingError
	if !errors.As(err, &pending) {
		return err
	}
	// Re-observe occupancy under the writer reservation. Only this request
	// proceeds to execution, regardless of how many observations it needed.
	wait, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	unknown := 0
	waiting := false
	for {
		err := p.verifyUnusedCrewRuntime(wait, cfg.ID, pending.ContainerID, &cfg)
		if err == nil {
			break
		}
		delay := time.Second
		if errors.Is(err, provider.ErrRuntimeOccupied) {
			if !waiting && cfg.ProvisionSink != nil {
				cfg.ProvisionSink(devcontainer.ProvisionEvent{Phase: devcontainer.ProvisionPhase, Step: devcontainer.ProvStepRuntimeDrain, Status: devcontainer.ProvStatusStarted})
			}
			waiting = true
			delay = 3 * time.Second
		} else if errors.Is(err, provider.ErrRuntimeObservation) {
			unknown++
			if unknown >= 3 {
				return err
			}
			delay *= time.Duration(unknown)
		} else {
			return err
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("%w: %w", err, wait.Err())
		case <-time.After(delay):
		}
	}
	return p.StopCrewRuntime(ctx, pending.ContainerID)
}

func (p *Provider) verifyUnusedCrewRuntime(ctx context.Context, crewID, containerID string, cfg *provider.CrewConfig) error {
	p.runtimeIdleMu.RLock()
	verify := p.runtimeIdleVerifier
	p.runtimeIdleMu.RUnlock()
	if verify == nil || p.cfg.InstanceID == "" {
		return fmt.Errorf("%w: runtime occupancy or installation ownership is unavailable", provider.ErrRuntimeImageUpdatePending)
	}
	inspected, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	c := inspected.Container
	if c.Config == nil || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.Labels[crewCrewIDLabel] != crewID || c.Config.Labels[crewKindLabel] != crewRuntimeKind || c.State == nil || !c.State.Running || c.State.Status != "running" || c.State.Dead || c.State.Paused || c.State.Restarting {
		return fmt.Errorf("%w: runtime ownership or state is unconfirmed", provider.ErrRuntimeImageUpdatePending)
	}
	if p.cfg.OwnerActive != nil {
		if err := p.cfg.OwnerActive(ctx, crewID); err != nil {
			return err
		}
	}
	// After server restart the start-specific fence cache is empty. Reconciliation
	// can request replacement before reaching its normal reuse/fence step.
	// Confirm the fence with the resolved configuration before the census exec.
	if cfg != nil {
		if err := p.verifyReplacementVolumes(ctx, c.Mounts, crewID, cfg.Slug); err != nil {
			return err
		}
		if err := p.ensureEgressFence(ctx, *cfg, containerID, c.Image); err != nil {
			return fmt.Errorf("%w: prepare fence for occupancy probe: %w", provider.ErrRuntimeImageUpdatePending, err)
		}
	}
	if err := verify(ctx, crewID, containerID); err != nil {
		return fmt.Errorf("%w: %w", provider.ErrRuntimeImageUpdatePending, err)
	}
	return ctx.Err()
}

// RetainCrewRuntimeUse guards callers that already have a concrete runtime.
// It does not admit a replacement or authorize access to the crew.
func (p *Provider) RetainCrewRuntimeUse(ctx context.Context, crewID, containerID string) (*provider.RuntimeUse, error) {
	if crewID == "" || containerID == "" {
		return nil, errors.New("runtime use requires crew and container identities")
	}
	release, err := p.runtimeUseGate(crewID).Use(ctx)
	if err != nil {
		return nil, err
	}
	inspected, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		release()
		return nil, err
	}
	c := inspected.Container
	if c.Config == nil || c.State == nil || !c.State.Running || c.State.Paused || c.State.Restarting {
		release()
		return nil, errors.New("runtime use refers to an inactive container")
	}
	label := c.Config.Labels[crewCrewIDLabel]
	if (label != "" && label != crewID) || (label == "" && (!strings.HasPrefix(strings.TrimPrefix(c.Name, "/"), p.namePrefix()+"-team-") || !strings.HasSuffix(c.Name, "-"+crewID))) {
		release()
		return nil, errors.New("runtime use refers to another crew")
	}
	return provider.NewRuntimeUse(crewID, c.ID, release), nil
}

func (p *Provider) StopUnusedCrewRuntime(ctx context.Context, crewID, containerID string) (bool, error) {
	if crewID == "" || containerID == "" {
		return false, errors.New("idle stop requires runtime identity")
	}
	unlock, ok := p.runtimeUseGate(crewID).TryExclusive()
	if !ok {
		return false, nil
	}
	defer unlock()
	if err := p.verifyUnusedCrewRuntime(ctx, crewID, containerID, nil); err != nil {
		return false, err
	}
	if err := p.StopCrewRuntime(ctx, containerID); err != nil {
		return false, err
	}
	return true, nil
}

func crewSlugFromInspect(cfg *container.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Labels[crewCrewLabel]
}

// Refuse replacement when an image or an operator attached a volume that
// the crew create path cannot reattach. Anonymous volumes can hold user data.
func (p *Provider) verifyReplacementVolumes(ctx context.Context, mounts []container.MountPoint, crew, slug string) error {
	for _, m := range mounts {
		if m.Type == mount.TypeBind && agentWritableNoexecTargets[m.Destination] && p.detected.Runtime == "podman" {
			// A legacy exec-capable bind may still retain data at the
			// expected host path. Refuse to switch to a different source.
			if m.Source != p.crewDataSource(crew, m.Destination) {
				return fmt.Errorf("%w: unexpected bind source at %s", provider.ErrRuntimeStorageUnverified, m.Destination)
			}
			continue
		}
		if m.Type != mount.TypeVolume {
			continue
		}
		if slug != "" && (m.Destination == "/home/agent" && m.Name == p.homeVolumeName(crew, slug) || m.Destination == "/opt/crew-tools" && m.Name == p.toolsVolumeName(crew, slug)) {
			continue
		}
		device := p.crewDataSource(crew, m.Destination)
		if device != "" && m.Name != "" {
			v, err := p.client.VolumeInspect(ctx, m.Name, client.VolumeInspectOptions{})
			if err != nil {
				return fmt.Errorf("%w: inspect persistent volume: %w", provider.ErrRuntimeImageUpdatePending, err)
			}
			if v.Volume.Name == m.Name && v.Volume.Driver == "local" && v.Volume.Options["type"] == "none" && v.Volume.Options["device"] == device && v.Volume.Options["o"] == noexecBindMountOpts {
				continue
			}
		}
		return fmt.Errorf("%w: existing volume at %s has no verified replacement mount", provider.ErrRuntimeStorageUnverified, m.Destination)
	}
	return nil
}

func (p *Provider) crewDataSource(crew, target string) string {
	var device string
	switch target {
	case "/workspace":
		device = filepath.Join(p.cfg.OutputBasePath, "workspaces", crew)
	case "/output":
		device = filepath.Join(p.cfg.OutputBasePath, crew)
	case "/crew":
		device = filepath.Join(p.cfg.OutputBasePath, "crews", crew)
	}
	return device
}

// Compare the effective mount and the engine's recorded configuration.
// Podman reports the normalized mount flags in HostConfig.Binds and can
// leave MountPoint.Mode empty. An anonymous name is never a host path.
func (p *Provider) verifiedPodmanDataBind(host *container.HostConfig, m container.MountPoint, crew string) bool {
	if p.detected.Runtime != "podman" || host == nil || m.Type != mount.TypeBind || !m.RW || m.Source != p.crewDataSource(crew, m.Destination) || !filepath.IsAbs(m.Source) {
		return false
	}
	matches := 0
	for _, spec := range host.Binds {
		fields := strings.Split(spec, ":")
		if len(fields) < 2 || fields[1] != m.Destination {
			continue
		}
		matches++
		if len(fields) != 3 || fields[0] != m.Source {
			return false
		}
		opts := strings.Split(fields[2], ",")
		if !slices.Contains(opts, "noexec") || !slices.Contains(opts, "nosuid") || slices.Contains(opts, "exec") || slices.Contains(opts, "suid") {
			return false
		}
	}
	return matches == 1
}
