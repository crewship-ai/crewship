//go:build !linux

package docker

import (
	"context"

	"github.com/moby/moby/api/types/container"
)

func (p *Provider) publishStagedCleanup(container.InspectResponse) error { return errStagedDenied }
func (p *Provider) finishStagedCleanup(container.InspectResponse) error  { return errStagedDenied }
func (p *Provider) reconcileStagedEnvCleanup(context.Context, int) error { return errStagedDenied }
