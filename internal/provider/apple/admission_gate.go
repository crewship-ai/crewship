package apple

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// admitContainerStart holds until the host can afford one more crew container.
// Mirror of the Docker provider's helper; see internal/provider/container.go's
// AdmissionGate doc for why the gate lives at this depth rather than at the
// call sites.
//
// This provider emits no container-preparation events — no start, no create,
// no ready — but it DOES emit the capacity hold (#1675). That asymmetry is
// deliberate. The other steps are an audit trail, and their absence costs a
// journal row. A hold is not an audit trail: it is the only step that can last
// thirty minutes, and while it lasts the caller's stream shows "Starting
// container..." and nothing else, which reads exactly like a hang. The memory
// leg of the gate is inactive on macOS (no /proc/meminfo), but the CONCURRENCY
// and PACING legs bind here exactly as they do on Linux, so a start on this
// provider can be held and the silence would be the same silence.
//
// The queryable surface (GET /api/v1/runtime/capacity and `crewship now`)
// reads the controller directly and is unaffected either way.
func (p *Provider) admitContainerStart(ctx context.Context, team provider.CrewConfig) (func(), error) {
	gate := p.cfg.Admission
	if gate == nil {
		return func() {}, nil
	}

	// Measured here rather than inside the gate so the event can say how long
	// the wait has been going: "held for capacity" reads the same at second
	// one and at minute twenty-five.
	asked := time.Now()

	release, err := gate.Admit(ctx, team.ID, team.Slug, func(reason, detail string) {
		waited := time.Since(asked)
		p.logger.Info("crew container start held for host capacity",
			"crew_id", team.ID, "crew_slug", team.Slug,
			"reason", reason, "detail", detail, "waited", waited.Round(time.Second))
		if team.ProvisionSink != nil {
			team.ProvisionSink(devcontainer.ProvisionEvent{
				Phase:  devcontainer.ProvisionPhase,
				Step:   devcontainer.ProvStepCapacityHold,
				Status: devcontainer.ProvStatusStarted,
				Reason: reason,
				Detail: detail,
				// Elapsed wait, not the duration of a completed step.
				DurationMs: waited.Milliseconds(),
			})
		}
	})
	if err != nil {
		// %w keeps *admission.HoldExpiredError reachable through errors.As,
		// which is what lets the chat classifier name the host resource that
		// ran out instead of matching "container start" out of this sentence.
		return nil, fmt.Errorf("admission control refused a container start for crew %s: %w", team.ID, err)
	}
	return release, nil
}
