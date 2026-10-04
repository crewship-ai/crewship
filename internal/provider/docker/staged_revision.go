package docker

import (
	"context"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"time"
)

// reconcileStagedRevision removes only a stopped exact-owned generation.
// The caller creates its replacement; persistent volumes are retained.
func (p *Provider) reconcileStagedRevision(ctx context.Context, team provider.CrewConfig, c container.InspectResponse) (removed bool, err error) {
	if c.State == nil || c.State.Status == "" {
		return false, fmt.Errorf("%w: existing keeper state unavailable", errStagedDenied)
	}
	e := p.auditStaged(ctx, c, team)
	if e == nil {
		return false, nil
	}
	exactOwned := staged(c) && c.ID != "" && p.cfg.InstanceID != "" && c.Config.Labels[resourcelifecycle.InstanceLabel] == p.cfg.InstanceID && c.Config.Labels[crewCrewIDLabel] == team.ID
	if !errors.Is(e, provider.ErrRuntimeImageUpdatePending) || !exactOwned {
		return false, fmt.Errorf("%w; runtime retained, workload admission refused; inspect ownership and drain/recreate the crew runtime", e)
	}
	state := c.State
	if state.Running || state.Paused || state.Restarting || (state.Status != "exited" && state.Status != "created") {
		p.evictWarm(team.ID)
		return false, e
	}
	if e := p.removeCrewContainer(ctx, c.ID, client.ContainerRemoveOptions{Force: false}); e != nil {
		return false, fmt.Errorf("replace stopped staged generation: %w", e)
	}
	p.evictWarm(team.ID)
	p.forgetFenced(c.ID)
	return true, nil
}

// stagedImageDrift resolves a local tag to compare immutable runtime identity.
func (p *Provider) stagedImageDrift(ctx context.Context, c container.InspectResponse, desired string) (bool, error) {
	if !staged(c) || desired == "" {
		return false, errStagedDenied
	}
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	image, err := p.client.ImageInspect(lookup, desired)
	if err != nil {
		return false, fmt.Errorf("resolve staged desired image: %w", err)
	}
	if image.ID == "" || c.Image == "" {
		return false, fmt.Errorf("%w: staged image identity unavailable", errStagedDenied)
	}
	return c.Image != image.ID, nil
}
