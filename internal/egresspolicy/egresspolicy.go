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

	"github.com/crewship-ai/crewship/internal/egressallow"
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
//     egressallow.DomainAllowlist semantics) must be one of
//     egressallow.DefaultAllowedDomains + the crew's allowed_domains. Exact-match
//     only, identical to the container proxy: every egress path sees one
//     boundary.
//   - unknown mode / DB error / malformed allowed_domains → block (fail
//     closed). An operator who set a policy we cannot read must never
//     silently get allow-all; mirrors the sidecar's unknown-mode handling.
//
// The query is one indexed PK SELECT ahead of a network round trip — a policy
// flip therefore applies to the very next request rather than waiting out a
// cache TTL.
//
// Check is the notify/hooks/general-purpose entry point — its "free mode
// allows anything" behaviour is UNCHANGED by CheckHTTPStep below (see
// httpStepOpts.enforceFreeFloor, which only CheckHTTPStep sets).
func Check(ctx context.Context, db *sql.DB, crewID, host string) error {
	return checkInternal(ctx, db, crewID, host, httpStepOpts{})
}

// httpStepOpts carries the two independent hardening dials pipeline http
// steps compose (#1416 items 1 & 3), kept OFF (zero value) for every other
// caller of checkInternal so Check's contract never changes underneath
// notify/hooks.
type httpStepOpts struct {
	// enforceFreeFloor, when true, additionally requires a free/unrestricted
	// crew's host to clear egressallow.DefaultAllowedDomains unless
	// routineDeclaresEgress is also true. Set only by CheckHTTPStep.
	enforceFreeFloor bool
	// routineDeclaresEgress reports that the CALLING routine's own
	// egress_targets is non-empty — runner_http.go's hostInEgressTargets is
	// the authoritative per-host gate for that declaration, so the crew-
	// policy floor steps aside rather than double-gating an explicit,
	// author-declared target.
	routineDeclaresEgress bool
	// forceRestricted, when true, treats the crew's network_mode as
	// 'restricted' regardless of what's actually stored — the webhook-
	// triggered-run override (#1416 item 1), mirroring the agent-webhook
	// path's NetworkMode="restricted" (internal/api/webhook.go).
	forceRestricted bool
}

// CheckHTTPStep is Check plus SSRF hardening scoped to pipeline http steps
// (#1416 items 1 & 3) — it does NOT change Check's own contract, so
// notify/hooks (which call Check, not this) are byte-for-byte unaffected.
//
//   - routineDeclaresEgress: pass true when the routine's own egress_targets
//     is non-empty. hostInEgressTargets already enforces that declaration
//     per-host; this only tells the crew-policy layer "the author opted in
//     to an explicit allowlist," so the free-mode floor below steps aside.
//   - forceRestricted: pass true for a webhook-triggered run. The crew's
//     network policy is evaluated as if it were 'restricted' — its own
//     allowed_domains (if any) still apply, on top of
//     egressallow.DefaultAllowedDomains — regardless of the crew's actual
//     network_mode. This is the "stronger" form of the free-mode floor:
//     even a routine that DOES declare egress_targets still has to clear
//     it, because the inbound payload driving this run is untrusted.
//
// Fix for the residual gap Check's "free mode allows anything" left open:
// a routine on a default `free` crew with an undeclared `{{ inputs.url }}`
// http step could previously reach ANY public host (private/link-local IPs
// stay blocked by httpsafe regardless — unaffected). An undeclared free-mode
// step is now held to the same floor 'restricted' mode enforces before any
// operator-curated allowed_domains are added.
func CheckHTTPStep(ctx context.Context, db *sql.DB, crewID, host string, routineDeclaresEgress, forceRestricted bool) error {
	return checkInternal(ctx, db, crewID, host, httpStepOpts{
		enforceFreeFloor:      true,
		routineDeclaresEgress: routineDeclaresEgress,
		forceRestricted:       forceRestricted,
	})
}

func checkInternal(ctx context.Context, db *sql.DB, crewID, host string, opts httpStepOpts) error {
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
	if opts.forceRestricted {
		// Webhook-triggered run: hold the crew to the 'restricted' floor
		// regardless of its actual network_mode. domainsJSON (the crew's
		// own allowed_domains column, whatever it holds) still applies on
		// top of the shared defaults — same as a genuinely-restricted crew.
		mode = "restricted"
	}
	switch mode {
	case "", "free":
		if !opts.enforceFreeFloor || opts.routineDeclaresEgress {
			return nil
		}
		if !egressallow.NewDomainAllowlist(egressallow.DefaultAllowedDomains).IsAllowed(host) {
			return fmt.Errorf("crew %q network policy is 'free' and this http step declares no egress_targets — add %q to the routine's egress_targets, or set the crew's network_mode to 'restricted' with allowed_domains, to permit this egress", crewID, host)
		}
		return nil
	case "restricted":
		domains := append([]string{}, egressallow.DefaultAllowedDomains...)
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
		if !egressallow.NewDomainAllowlist(domains).IsAllowed(host) {
			return fmt.Errorf("crew %q network policy is 'restricted' and its allowed_domains do not include host %q — add it to the crew's allowed domains to permit egress", crewID, host)
		}
		return nil
	default:
		// Unknown mode in the DB (schema drift / manual SQL): fail closed,
		// same stance as the sidecar proxy.
		return fmt.Errorf("crew %q has unknown network_mode %q (failing closed)", crewID, mode)
	}
}
