package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moby/moby/client"

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
	for {
		release, err := gate.Use(ctx)
		if err != nil {
			return nil, err
		}
		id, err := p.EnsureCrewRuntime(ctx, cfg)
		if err == nil {
			return provider.NewRuntimeUse(cfg.ID, id, release), nil
		}
		release()
		var pending *provider.RuntimeImageUpdatePendingError
		if !errors.As(err, &pending) {
			return nil, err
		}
		if provider.RuntimeUseNoWait(ctx) {
			return nil, err
		}
		unlock, err := gate.Exclusive(ctx)
		if err != nil {
			return nil, err
		}
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
	if err := p.verifyUnusedCrewRuntime(ctx, cfg.ID, pending.ContainerID); err != nil {
		return err
	}
	return p.StopCrewRuntime(ctx, pending.ContainerID)
}

func (p *Provider) verifyUnusedCrewRuntime(ctx context.Context, crewID, containerID string) error {
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
		if err := p.cfg.OwnerActive(ctx, p.cfg.InstanceID); err != nil {
			return err
		}
	}
	if err := verify(ctx, crewID, containerID); err != nil {
		return fmt.Errorf("%w: %v", provider.ErrRuntimeImageUpdatePending, err)
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
	if err := p.verifyUnusedCrewRuntime(ctx, crewID, containerID); err != nil {
		return false, err
	}
	if err := p.StopCrewRuntime(ctx, containerID); err != nil {
		return false, err
	}
	return true, nil
}
