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

// actingAgentID resolves the acting agent's ID for provenance/attribution on
// routes that record "which agent did this" (issue authorship, port-expose,
// pipeline authoring, keeper requests, …). It layers over actingIdentity:
//
//   - a valid per-agent token overrides the boot identity → (tokenAgentID, true)
//   - an unrecognized token is a forgery → ("", false); callers 403
//   - no token → fall back to the boot identity (s.ipc.AgentID) → (bootID, true)
//
// This keeps every ambient-identity route consistent: a shared-container
// sibling can no longer have its action attributed to (or performed as) the
// boot agent, while legacy callers that carry no token keep working.
func (s *Server) actingAgentID(r *http.Request) (id string, ok bool) {
	actorID, _, present, matched := s.actingIdentity(r)
	if !present {
		if s.ipc != nil {
			return s.ipc.AgentID, true
		}
		return "", true
	}
	if !matched {
		return "", false
	}
	return actorID, true
}
