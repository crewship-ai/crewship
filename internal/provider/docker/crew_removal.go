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
	scope, known := p.stagedEnvScopes.Load(id)
	if known {
		if err := p.publishStagedCleanup(scope.(container.InspectResponse)); err != nil {
			return fmt.Errorf("persist runtime cleanup intent: %w", err)
		}
	}
	if _, err := p.client.ContainerRemove(ctx, id, opts); err != nil && !(known && errdefs.IsNotFound(err)) {
		return err
	}
	if known {
		if err := p.finishStagedCleanup(scope.(container.InspectResponse)); err != nil {
			return fmt.Errorf("clean removed runtime material: %w", err)
		}
	}
	// Missing authentic scope retains an orphan for explicit reconciliation.
	// No additional inspect is introduced on legacy removals or caller aliases.
	return nil
}
