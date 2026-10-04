package docker

import (
	"context"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
)

func (p *Provider) stagedLegacyCheck(ctx context.Context, team provider.CrewConfig) error {
	old, e := p.HasLegacyCrewResources(ctx, []provider.CrewRef{{ID: team.ID, Slug: team.Slug}})
	if e != nil {
		return e
	}
	if old {
		return fmt.Errorf("%w: legacy resources require an explicit drained migration", errStagedDenied)
	}
	return nil
}
