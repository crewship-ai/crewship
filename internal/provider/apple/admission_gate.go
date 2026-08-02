package apple

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/provider"
)

// admitContainerStart holds until the host can afford one more crew container.
// Mirror of the Docker provider's helper; see internal/provider/container.go's
// AdmissionGate doc for why the gate lives at this depth rather than at the
// call sites.
//
// This provider has no ProvisionSink, so a hold is reported to the log only.
// The queryable surface (GET /api/v1/runtime/capacity and `crewship now`)
// reads the controller directly and is unaffected.
func (p *Provider) admitContainerStart(ctx context.Context, team provider.CrewConfig) (func(), error) {
	gate := p.cfg.Admission
	if gate == nil {
		return func() {}, nil
	}
	release, err := gate.Admit(ctx, team.ID, team.Slug, func(reason, detail string) {
		p.logger.Info("crew container start held for host capacity",
			"crew_id", team.ID, "crew_slug", team.Slug, "reason", reason, "detail", detail)
	})
	if err != nil {
		return nil, fmt.Errorf("admission control refused a container start for crew %s: %w", team.ID, err)
	}
	return release, nil
}
