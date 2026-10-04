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

// Prepare the exact new runtime before start; roll back even on cancellation.
// Persistent binds and named volumes survive failed preparation.
func (p *Provider) prepareCreatedStaged(ctx context.Context, id string, cfg *container.Config, env []string, team provider.CrewConfig) (err error) {
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if e := p.removeCrewContainer(cleanup, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); e != nil {
			err = errors.Join(err, fmt.Errorf("remove failed staged create: %w", e))
			return // Material is still needed while Docker retains the runtime.
		}
	}()
	got, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	c := got.Container
	if c.ID != id || c.Image != cfg.Image || c.Config == nil || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.Labels[crewCrewIDLabel] != team.ID || c.Config.Labels[stagedEnvLabel] != cfg.Labels[stagedEnvLabel] {
		return fmt.Errorf("%w: created runtime identity mismatch", errStagedDenied)
	}
	if err = p.saveStagedEnv(c, env); err != nil {
		return err
	}
	return p.auditStaged(ctx, c, team)
}
