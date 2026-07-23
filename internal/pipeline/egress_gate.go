package pipeline

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/egresspolicy"
)

// NewCrewNetworkPolicyGate builds the production egress gate for http
// steps from the crew network policy — crews.network_mode +
// crews.allowed_domains (migration v18), the SAME policy source the
// sidecar proxy enforces for agent_run container egress. The full
// layer-by-layer semantics (free/restricted/fail-closed) live in
// egresspolicy.Check, the one source http steps, notify, and hooks all
// resolve through — reusing it keeps one security dial per crew: a crew
// whose agents are restricted to a domain set cannot exfiltrate through a
// routine's direct http step instead.
//
// The gate queries per call (http steps + each redirect hop). That is
// one indexed PK SELECT ahead of a network round trip — negligible —
// and it means a policy flip applies to the very next request instead
// of waiting out a cache TTL.
func NewCrewNetworkPolicyGate(db *sql.DB) func(ctx context.Context, scope RunScope, host string) error {
	return func(ctx context.Context, scope RunScope, host string) error {
		// The http-step gate is one of three app-layer egress paths (notify
		// and hooks are the others) that all resolve the crew boundary
		// through the shared egresspolicy source, keyed on the authoring
		// crew — no per-path divergence.
		return egresspolicy.Check(ctx, db, scope.AuthorCrewID, host)
	}
}
