package server

// The server's handle on the crew-start contract (internal/crewstart).
//
// The two internal start routes (POST /crews/{id}/container/start and
// POST /agents/{id}/start) each assembled a provider.CrewConfig by hand from
// what the request happened to carry, and neither ever read the crews row. A
// crew therefore started from the global default runtime image rather than its
// provisioned devcontainer (#1717) and without the sidecar services it declares
// (#1708) — silently, in both cases.
//
// The Starter is built per call rather than held on the Server on purpose: the
// container provider is a field that tests (and the boot sequence) may replace
// after New, and a Starter captured at construction would keep starting crews
// through the provider that has since been swapped out.

import (
	"context"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/provider"
)

// startCrew creates-or-reuses the crew's container and brings its declared
// sidecars up, resolving whatever the caller left unset from the crews row.
func (s *Server) startCrew(ctx context.Context, cfg provider.CrewConfig) (string, error) {
	return s.crewStarter().Start(ctx, cfg)
}

func (s *Server) crewStarter() *crewstart.Starter {
	return crewstart.New(s.container, goapi.NewCrewConfigCompleter(s.db), s.logger)
}
