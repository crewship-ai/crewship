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
// egresspolicy.CheckHTTPStep, which wraps the shared egresspolicy.Check
// notify/hooks also resolve through — CheckHTTPStep layers TWO pipeline-
// http-step-only hardening pieces on top (#1416 items 1 & 3) without
// changing Check's own contract for notify/hooks:
//
//   - a free/unrestricted crew's undeclared http step is held to the same
//     floor 'restricted' mode enforces (item 3 — SSRF to arbitrary public
//     hosts);
//   - a webhook-triggered run's http steps are held to the 'restricted'
//     floor regardless of the crew's actual mode (item 1 — untrusted
//     inbound payload).
//
// Both dials are threaded through RunScope (WebhookTriggered,
// RoutineDeclaresEgress) rather than the gate signature, so this stays a
// drop-in replacement for every existing WithEgressGate caller/test.
//
// The gate queries per call (http steps + each redirect hop). That is
// one indexed PK SELECT ahead of a network round trip — negligible —
// and it means a policy flip applies to the very next request instead
// of waiting out a cache TTL.
func NewCrewNetworkPolicyGate(db *sql.DB) func(ctx context.Context, scope RunScope, host string) error {
	return func(ctx context.Context, scope RunScope, host string) error {
		return egresspolicy.CheckHTTPStep(ctx, db, scope.AuthorCrewID, host, scope.RoutineDeclaresEgress, scope.WebhookTriggered)
	}
}
