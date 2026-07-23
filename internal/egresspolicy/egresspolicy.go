// Package egresspolicy is the single crew network-policy source shared by
// every non-container outbound path — routine http steps, notify/webhook
// channels, and hooks. It resolves crews.network_mode + crews.allowed_domains
// (migration v18) — the SAME dial the agent-container sidecar proxy enforces
// for in-container egress — so a crew whose agents are restricted to a domain
// set cannot exfiltrate through whichever app-layer path an author reaches
// for instead. Centralizing it here (rather than re-deriving the semantics
// per path) is what keeps one security dial per crew with no per-path
// divergence.
package egresspolicy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

// Check enforces the crew egress policy for a single outbound host. nil =
// allow, non-nil = block (the error text names the fix). Semantics mirror
// the sidecar proxy + pipeline egress gate, layer by layer:
//
//   - empty crewID            → allow. The caller has no crew scope (dry-run
//     / draft / system paths). The path's own SSRF guard still applies.
//   - db == nil               → allow. The crew-policy layer is simply not
//     wired (bare unit paths). Production callers always pass the control-
//     plane DB; the per-path SSRF guard is unaffected either way.
//   - crew row missing/deleted → allow. Matches the v18 column DEFAULT and
//     the policy-resolver convention of a safe default for a crew deleted
//     mid-operation.
//   - network_mode ""/"free"  → allow. The backward-compat default for every
//     crew that never opted into restriction — wiring this gate must not
//     break existing notify/hook/http egress of crews with no policy.
//   - network_mode "restricted" → host (port stripped, case normalized —
//     sidecar.DomainAllowlist semantics) must be one of
//     sidecar.DefaultAllowedDomains + the crew's allowed_domains. Exact-match
//     only, identical to the container proxy: every egress path sees one
//     boundary.
//   - unknown mode / DB error / malformed allowed_domains → block (fail
//     closed). An operator who set a policy we cannot read must never
//     silently get allow-all; mirrors the sidecar's unknown-mode handling.
//
// The query is one indexed PK SELECT ahead of a network round trip — a policy
// flip therefore applies to the very next request rather than waiting out a
// cache TTL.
func Check(ctx context.Context, db *sql.DB, crewID, host string) error {
	if crewID == "" || db == nil {
		return nil
	}
	var mode string
	var domainsJSON sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(network_mode, 'free'), allowed_domains
		   FROM crews
		  WHERE id = ? AND deleted_at IS NULL`,
		crewID,
	).Scan(&mode, &domainsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("crew network policy lookup for crew %q failed (failing closed): %w", crewID, err)
	}
	switch mode {
	case "", "free":
		return nil
	case "restricted":
		domains := append([]string{}, sidecar.DefaultAllowedDomains...)
		if domainsJSON.Valid && domainsJSON.String != "" {
			var crewDomains []string
			if jerr := json.Unmarshal([]byte(domainsJSON.String), &crewDomains); jerr != nil {
				// Malformed allowed_domains on a RESTRICTED crew: the operator
				// asked for an allowlist we cannot read. Fail closed with the
				// fix in the message rather than silently defaulting to
				// defaults-only or allow-all.
				return fmt.Errorf("crew %q has network_mode=restricted but malformed allowed_domains (failing closed): %v", crewID, jerr)
			}
			domains = append(domains, crewDomains...)
		}
		if !sidecar.NewDomainAllowlist(domains).IsAllowed(host) {
			return fmt.Errorf("crew %q network policy is 'restricted' and its allowed_domains do not include host %q — add it to the crew's allowed domains to permit egress", crewID, host)
		}
		return nil
	default:
		// Unknown mode in the DB (schema drift / manual SQL): fail closed,
		// same stance as the sidecar proxy.
		return fmt.Errorf("crew %q has unknown network_mode %q (failing closed)", crewID, mode)
	}
}
