package docker

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// admitContainerStart holds this goroutine until the host can afford one more
// crew container, and returns the release.
//
// Called from exactly two places, both of which make a container resident:
// the create in EnsureCrewRuntime, and the start of a stopped container in
// reconcileExistingContainer. Not from the warm path, and not from the reuse
// of an already-running container — neither adds anything to the host, and
// gating them would mean a busy host stops answering chats to crews that are
// already up.
//
// A nil gate is a no-op with a no-op release, so a Provider built without
// admission control (every existing test, and any deployment that has not
// wired one) behaves exactly as it did before.
//
// The hold is reported onto the run's own provisioning stream. That stream is
// journaled and streamed over the session WS, so "waiting for capacity"
// arrives on the surfaces an operator is already watching rather than only in
// the daemon log — a queue nobody can see is indistinguishable from a hang.
func (p *Provider) admitContainerStart(
	ctx context.Context,
	team provider.CrewConfig,
	emitProv func(devcontainer.ProvisionEvent),
) (func(), error) {
	gate := p.cfg.Admission
	if gate == nil {
		return func() {}, nil
	}

	release, err := gate.Admit(ctx, team.ID, team.Slug, func(reason, detail string) {
		p.logger.Info("crew container start held for host capacity",
			"crew_id", team.ID, "crew_slug", team.Slug, "reason", reason, "detail", detail)
		if emitProv != nil {
			emitProv(devcontainer.ProvisionEvent{
				Step:   devcontainer.ProvStepCapacityHold,
				Status: devcontainer.ProvStatusStarted,
				Detail: fmt.Sprintf("%s: %s", reason, detail),
			})
		}
	})
	if err != nil {
		// Wrapped, not swallowed: a start that was held until its context ran
		// out has to fail as a capacity problem, not as a generic timeout the
		// operator will read as a broken daemon.
		return nil, fmt.Errorf("admission control refused a container start for crew %s: %w", team.ID, err)
	}
	return release, nil
}
