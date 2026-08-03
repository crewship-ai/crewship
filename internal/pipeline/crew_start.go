package pipeline

// The pipeline runner's handle on the crew-start contract
// (internal/crewstart).
//
// Three of the thirteen pre-#1708 crew starts were here — the prewarm, the
// agent step and the script step — and the three disagreed with each other as
// well as with chat: the prewarm resolved the crew's config and swallowed a
// failure, the script step resolved it and hard-failed, and the agent step
// never resolved it at all, hand-building a config from the resolved agent.
// None of them started the crew's declared sidecars, so a routine that ran a
// step against a crew with `services: [postgres]` executed against nothing.

import (
	"context"

	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/provider"
)

// startCrew creates-or-reuses the crew's container and brings its declared
// sidecars up, returning the container id and the config the crew was actually
// started with (the caller registers the effective TTL with the reaper).
//
// The completer is the injected crewRuntime resolver — internal/pipeline cannot
// import internal/api (which imports it), so the DB-backed resolution arrives
// as a closure from cmd_start. With no resolver wired the caller's config is
// used as-is, which is the pre-existing fallback.
func (r *OrchestratorRunner) startCrew(ctx context.Context, cfg provider.CrewConfig, workspaceID string) (string, provider.CrewConfig, error) {
	var completer crewstart.Completer
	if r.crewRuntime != nil {
		completer = crewstart.CompleterFunc(func(ctx context.Context, c provider.CrewConfig) (provider.CrewConfig, error) {
			return r.crewRuntime(ctx, c.ID, workspaceID)
		})
	}
	return crewstart.New(r.container, completer, r.logger).StartResolved(ctx, cfg, nil)
}
