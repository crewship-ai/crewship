package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

// NewCrewNetworkPolicyGate builds the production egress gate for http
// steps from the crew network policy — crews.network_mode +
// crews.allowed_domains (migration v18), the SAME policy source the
// sidecar proxy enforces for agent_run container egress and
// internal/api/agent_config.go resolves for container boot. Reusing it
// (rather than inventing a pipeline-only allowlist) keeps one security
// dial per crew: a crew whose agents are restricted to a domain set
// cannot exfiltrate through a routine's direct http step instead.
//
// Semantics, layer by layer (mirrors resolveNetworkPolicy + sidecar):
//
//   - no author crew on the run  → allow. Only dry-run/draft paths can
//     reach here without a crew (Run always stamps AuthorCrewID, and
//     live RunDefinition requires it); the routine egress_targets check
//     and the httpsafe SSRF guard still apply.
//   - crew row missing           → allow. Matches the v18 column
//     DEFAULT 'free' (a crew that never set a policy is free) and the
//     policy-resolver convention of a safe default for a crew deleted
//     mid-operation.
//   - network_mode 'free'/empty  → allow (the default for every crew
//     that never opted into restriction — this is the backward-compat
//     contract: wiring this gate must not break http routines of crews
//     that never set a network policy).
//   - network_mode 'restricted'  → the host (port stripped, case
//     normalized — sidecar.DomainAllowlist semantics) must be one of
//     sidecar.DefaultAllowedDomains + the crew's allowed_domains.
//     Exact-match only, same as the container proxy: agent_run and
//     http steps see the identical boundary.
//   - unknown mode / DB error    → block (fail closed). An operator
//     who set a policy we can't read must not silently get allow-all;
//     mirrors sidecar/server.go's unknown-mode handling.
//
// The gate queries per call (http steps + each redirect hop). That is
// one indexed PK SELECT ahead of a network round trip — negligible —
// and it means a policy flip applies to the very next request instead
// of waiting out a cache TTL.
func NewCrewNetworkPolicyGate(db *sql.DB) func(ctx context.Context, scope RunScope, host string) error {
	return func(ctx context.Context, scope RunScope, host string) error {
		if scope.AuthorCrewID == "" {
			return nil
		}
		var mode string
		var domainsJSON sql.NullString
		err := db.QueryRowContext(ctx,
			`SELECT COALESCE(network_mode, 'free'), allowed_domains
			   FROM crews
			  WHERE id = ? AND deleted_at IS NULL`,
			scope.AuthorCrewID,
		).Scan(&mode, &domainsJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("crew network policy lookup for crew %q failed (failing closed): %w", scope.AuthorCrewID, err)
		}
		switch mode {
		case "", "free":
			return nil
		case "restricted":
			domains := append([]string{}, sidecar.DefaultAllowedDomains...)
			if domainsJSON.Valid && domainsJSON.String != "" {
				var crewDomains []string
				if jerr := json.Unmarshal([]byte(domainsJSON.String), &crewDomains); jerr != nil {
					// Malformed allowed_domains on a RESTRICTED crew: the
					// operator asked for an allowlist we cannot read. Fail
					// closed with the fix in the message rather than
					// silently running with defaults only or allow-all.
					return fmt.Errorf("crew %q has network_mode=restricted but malformed allowed_domains (failing closed): %v", scope.AuthorCrewID, jerr)
				}
				domains = append(domains, crewDomains...)
			}
			if !sidecar.NewDomainAllowlist(domains).IsAllowed(host) {
				return fmt.Errorf("authoring crew's network policy is 'restricted' and its allowed_domains do not include this host — add it to the crew's allowed domains to permit http-step egress")
			}
			return nil
		default:
			// Unknown mode in the DB (schema drift / manual SQL): fail
			// closed, same stance as the sidecar proxy.
			return fmt.Errorf("crew %q has unknown network_mode %q (failing closed)", scope.AuthorCrewID, mode)
		}
	}
}
