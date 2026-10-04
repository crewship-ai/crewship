package docker

import (
	"context"

	"github.com/moby/moby/client"
)

// removeCrewContainer centralizes crew runtime removal without changing the
// caller's force or volume policy. Temporary helper containers use the client
// directly because they are not crew runtimes.
func (p *Provider) removeCrewContainer(ctx context.Context, id string, opts client.ContainerRemoveOptions) error {
	_, err := p.client.ContainerRemove(ctx, id, opts)
	return err
}
