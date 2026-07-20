package sidecar

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header. Returns "" when the header is absent or not a bearer scheme. The
// scheme match is case-insensitive per RFC 7235.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const pfx = "bearer "
	if len(h) >= len(pfx) && strings.EqualFold(h[:len(pfx)], pfx) {
		return strings.TrimSpace(h[len(pfx):])
	}
	return ""
}

// actingIdentity resolves the ACTING agent behind a sidecar call from the
// per-agent bearer token the orchestrator mints
// (internaltoken.DeriveAgentToken = HMAC(master, workspaceID‖agentID)) and
// injects into each agent's env + MCP config (#812).
//
// The shared per-crew sidecar can't trust a caller-supplied `from`/slug: every
// agent in the crew shares one container, so any of them can POST `from=<peer>`
// and impersonate a sibling (#796 could only reject slugs OUTSIDE the crew).
// The bearer token can't be spoofed that way — it is delivered per agent and
// each token maps, by constant-time equality against the roster the sidecar was
// booted with, to exactly one crew member.
//
// Return contract:
//   - present=false            → no bearer token on the request. Legacy caller;
//     routes fall back to their #796 membership-validated behaviour.
//   - present=true, ok=false   → a token was presented but matches no crew
//     member (forged/stale). Routes MUST reject (fail closed).
//   - present=true, ok=true    → token maps to (agentID, slug); this is the
//     authoritative acting identity and overrides any request `from`/URL slug.
func (s *Server) actingIdentity(r *http.Request) (agentID, slug string, present, ok bool) {
	tok := bearerToken(r)
	if tok == "" {
		return "", "", false, false
	}
	// The boot agent — the one this sidecar's IPC config was minted for.
	if s.ipc != nil && s.ipc.AgentToken != "" &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(s.ipc.AgentToken)) == 1 {
		return s.ipc.AgentID, s.ipc.AgentSlug, true, true
	}
	// Any other crew member sharing this container/sidecar.
	for i := range s.crewMembers {
		m := &s.crewMembers[i]
		if m.AuthToken != "" &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(m.AuthToken)) == 1 {
			return m.ID, m.Slug, true, true
		}
	}
	return "", "", true, false
}

// tokensProvisioned reports whether this sidecar was booted with any per-agent
// auth token (the boot agent or any peer). When true, per-agent identity is IN
// FORCE for the crew, so a request that presents NO token is a downgrade
// attempt — a sibling omitting the Authorization header to fall through to the
// spoofable membership check — and must be refused rather than accepted. Only a
// genuinely token-less (legacy / un-upgraded) deployment has this return false,
// where the #796 membership fallback still applies for backward compatibility.
func (s *Server) tokensProvisioned() bool {
	if s.ipc != nil && s.ipc.AgentToken != "" {
		return true
	}
	for i := range s.crewMembers {
		if s.crewMembers[i].AuthToken != "" {
			return true
		}
	}
	return false
}

// tokenlessDowngrade reports whether r is a downgrade attempt: it presents NO
// per-agent bearer token on a sidecar where per-agent tokens ARE provisioned.
//
// What this function guarantees is narrow, and the previous version of this
// comment overstated it. It guarantees only that every route which CALLS it
// applies the SAME predicate — one definition of "downgrade", not five
// slightly different re-derivations of "no token && tokensProvisioned()". It
// does NOT guarantee that a route calls it at all. #1274 claimed otherwise and
// was wrong in the same commit: it added the call to the memory MCP handler
// while the five legacy /memory/{read,write,search,status,reindex} routes
// registered ten lines away in buildHandler never called it and stayed exposed
// (CRE-153).
//
// Coverage is enforced elsewhere, by construction rather than by convention:
//
//   - the memory surface is gated in buildHandler by path prefix
//     (refuseUnauthorizedMemory, internal/sidecar/memory_guard.go) BEFORE the
//     route switch runs, so a new /memory or /mcp/memory route inherits the
//     check from registration alone;
//   - /query and /escalate still call this predicate inline — they are NOT
//     behind a prefix gate, and adding a sibling route next to them will not
//     pick the check up automatically;
//   - TestSidecarRoutes_IdentityCoverage (memory_routes_coverage_test.go)
//     enumerates the routes registered in buildHandler and fails when one is
//     unclassified, so the next route cannot silently reintroduce the class.
//
// A request with a token — valid or forged — is NOT a downgrade. This predicate
// answers exactly one question: "did the caller omit the header on a crew that
// issues tokens?" It says nothing about whether the token is real.
//
// That distinction was previously written here as "the caller resolves it
// through actingIdentity, which refuses forgeries on its own" — an assertion
// about callers that the five legacy /memory/* handlers did not honour, since
// they call actingIdentity nowhere. A forged token was therefore not a
// downgrade, not a forgery-refusal either, and simply passed. Forgery refusal
// now happens at the memory chokepoint (refuseUnauthorizedMemory) rather than
// being assumed of whoever calls this; do not restore a claim here about what
// callers do, because a comment cannot enforce it.
func (s *Server) tokenlessDowngrade(r *http.Request) bool {
	return bearerToken(r) == "" && s.tokensProvisioned()
}

// actingAgentID resolves the acting agent's ID for provenance/attribution on
// routes that record "which agent did this" (issue authorship, port-expose,
// pipeline authoring, keeper requests, …). It layers over actingIdentity:
//
//   - a valid per-agent token overrides the boot identity → (tokenAgentID, true)
//   - an unrecognized token is a forgery → ("", false); callers 403
//   - no token, but the crew HAS tokens → downgrade attempt → ("", false); 403
//   - no token and NO tokens provisioned (legacy) → boot identity → (bootID, true)
//
// This keeps every ambient-identity route consistent: a shared-container
// sibling can no longer have its action attributed to (or performed as) the
// boot agent, and — once tokens are provisioned — cannot drop the token to slip
// back into the boot identity either. Only a fully token-less deployment falls
// back.
func (s *Server) actingAgentID(r *http.Request) (id string, ok bool) {
	actorID, _, present, matched := s.actingIdentity(r)
	if !present {
		if s.tokensProvisioned() {
			return "", false
		}
		// Legacy (token-less) fallback: attribute to the boot agent — but only
		// when there IS a boot identity. When s.ipc is nil or its AgentID is
		// empty we CANNOT attribute the action, so fail closed with ("", false)
		// rather than the old ("", true), which conflated "no identity" with
		// "resolved" — a latent fail-open for any future caller that doesn't
		// pre-check ipc (#1059).
		if s.ipc != nil && s.ipc.AgentID != "" {
			return s.ipc.AgentID, true
		}
		return "", false
	}
	if !matched {
		return "", false
	}
	return actorID, true
}
