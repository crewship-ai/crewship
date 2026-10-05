package docker

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"

	"github.com/moby/moby/client"
)

// removeCrewContainer centralizes crew runtime removal without changing the
// caller's force or volume policy. Temporary helper containers use the client
// directly because they are not crew runtimes.
func (p *Provider) removeCrewContainer(ctx context.Context, id string, opts client.ContainerRemoveOptions) error {
	// A removal attempt invalidates reusable admission evidence, but an
	// unknown/failed outcome must retain selector-independent exec routing.
	p.fenced.Delete(id)
	p.stagedVerified.Delete(id)
	p.evictWarmContainer(id)
	scope, known := p.stagedEnvScopes.Load(id)
	if known {
		if err := p.publishStagedCleanup(scope.(container.InspectResponse)); err != nil {
			return fmt.Errorf("persist runtime cleanup intent: %w", err)
		}
	}
	if _, err := p.client.ContainerRemove(ctx, id, opts); err != nil && !(known && errdefs.IsNotFound(err)) {
		return err
	}
	// Only a confirmed deletion/absence permits forgetting the gate. Secret
	// cleanup may still fail; its durable intent remains independently owned.
	p.forgetFenced(id)
	if known {
		if err := p.finishStagedCleanup(scope.(container.InspectResponse)); err != nil {
			return fmt.Errorf("clean removed runtime material: %w", err)
		}
	}
	// Missing authentic scope retains an orphan for explicit reconciliation.
	// No additional inspect is introduced on legacy removals or caller aliases.
	return nil
}
