package sidecar

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
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
	agentID, slug, _, present, ok = s.actingRunIdentity(r)
	return agentID, slug, present, ok
}

// actingRunIdentity is actingIdentity plus the RUN the call belongs to (E0).
// runID is "" for a v1 (per-agent) token, which carries no run — every route
// that uses it must therefore treat "" as "unknown run", never as an error.
//
// Same return contract as actingIdentity for present/ok, with one addition: a
// token that verifies but names a run the orchestrator has told us ENDED is
// present=true, ok=FALSE. A finished run's credentials must stop working;
// otherwise a zombie runtime — a detached tmux CLI that outlived its run, which
// orchestrator_run.go's agentExecStillRunning path says happens routinely —
// keeps writing memory and raising escalations as a live agent.
func (s *Server) actingRunIdentity(r *http.Request) (agentID, slug, runID string, present, ok bool) {
	tok := bearerToken(r)
	if tok == "" {
		return "", "", "", false, false
	}
	return s.identityForRunTokenCtx(r.Context(), tok)
}

// identityForRunToken resolves a bearer token to an identity by TWO different
// mechanisms, in order:
//
//  1. Cryptographic verification of a per-run token (agtv2) against the crew's
//     run key. Nothing needs to be rostered for this to work, which is the
//     entire point: the roster is frozen at the moment the sidecar booted, so a
//     run that started afterwards can never be in it. This path admits that run
//     without a restart — and a restart is not a fallback, because it would end
//     every other run sharing the container.
//
//  2. Constant-time equality against the boot roster (agtv1), unchanged. It
//     still covers the boot agent and every crew member, and it is what a
//     mid-upgrade container — an orchestrator that already mints v2 talking to
//     a sidecar binary that predates it, or the reverse — falls back to.
//
// Order matters only for clarity; the two token formats are domain-separated in
// the MAC, so a v1 token can never satisfy the v2 check or vice versa.
func (s *Server) identityForRunToken(tok string) (agentID, slug, runID string, present, ok bool) {
	return s.identityForRunTokenCtx(context.Background(), tok)
}

// identityForRunTokenCtx is identityForRunToken with the request's context, so
// the run registry's authority probe (run_registry.go) is bounded by the call
// it is deciding about rather than outliving it. identityForRunToken keeps the
// context-free signature for the callers — and the tests — that have none.
func (s *Server) identityForRunTokenCtx(ctx context.Context, tok string) (agentID, slug, runID string, present, ok bool) {
	if tok == "" {
		return "", "", "", false, false
	}

	// (1) Verified per-run token.
	if s.ipc != nil && s.ipc.AgentRunKey != "" {
		// Restore the durable registry — live runs AND revocations — before
		// this function answers its first admission question for this process.
		// R7: a revocation that lives only in the ended process's heap is not a
		// revocation, and "unknown" must not silently mean "current" for a run
		// this sidecar ended before it restarted.
		s.runs.ensureDurable(s.ipc, s.logger)
		if ws, ag, run, valid := internaltoken.ValidateAgentRunToken(s.ipc.AgentRunKey, tok); valid {
			// The run key is already crew-scoped, so a token verifying under it
			// belongs to this crew. The workspace check is belt and braces
			// against a key reused across workspaces by a future caller.
			if s.ipc.WorkspaceID != "" && ws != s.ipc.WorkspaceID {
				return "", "", "", true, false
			}
			if !s.runs.current(ctx, run) {
				return "", "", run, true, false
			}
			// First sight of a run this sidecar did not boot with: record it,
			// so its identity is stable for the rest of the run and so the
			// registry can later be told it ended. The chat is unknown on this
			// path (the token binds workspace/agent/run and deliberately not a
			// chat id, which is not identity), so runChatID falls back to the
			// boot chat until the orchestrator supplies one.
			s.registerRunFromToken(run, ag)
			return ag, s.slugForAgentID(ag, run), run, true, true
		}
	}

	// (2) Boot roster.
	if s.ipc != nil && s.ipc.AgentToken != "" &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(s.ipc.AgentToken)) == 1 {
		return s.ipc.AgentID, s.ipc.AgentSlug, "", true, true
	}
	for i := range s.crewMembers {
		m := &s.crewMembers[i]
		if m.AuthToken != "" &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(m.AuthToken)) == 1 {
			return m.ID, m.Slug, "", true, true
		}
	}
	return "", "", "", true, false
}

// registerRunFromToken records a run the first time one of its calls arrives.
// Cheap and idempotent: runRegistry.start is a no-op for a run already known,
// and refuses to resurrect one already ended.
//
// This is what keeps the registry populated WITHOUT an inbound control channel
// from crewshipd, which does not exist — the sidecar only ever calls out (see
// s.ipc.BaseURL callers). A run announces itself by using its own token.
func (s *Server) registerRunFromToken(runID, agentID string) {
	if _, known := s.runs.lookup(runID); known {
		return
	}
	s.runs.start(runID, runState{AgentID: agentID, AgentSlug: s.slugForAgentID(agentID, "")})
	s.runs.sweep()
	s.runs.compact()
}

// slugForAgentID resolves a verified agent id to its slug. A v2 token binds the
// agent ID — which is what authorization needs — but not the slug, which is
// only ever used for display and for the peer-addressing surface.
//
// Three sources, in descending order of how much they know: the run's own
// registry entry (the orchestrator told us when the run started), the boot
// roster, then nothing. Returning "" is acceptable and deliberate: no route
// authorizes on the slug, so an unknown slug degrades a label, not a decision.
func (s *Server) slugForAgentID(agentID, runID string) string {
	if st, known := s.runs.lookup(runID); known && st.AgentSlug != "" {
		return st.AgentSlug
	}
	if s.ipc != nil && s.ipc.AgentID == agentID && s.ipc.AgentSlug != "" {
		return s.ipc.AgentSlug
	}
	for i := range s.crewMembers {
		if s.crewMembers[i].ID == agentID {
			return s.crewMembers[i].Slug
		}
	}
	return ""
}

// requestChatID is the chat THIS REQUEST belongs to.
//
// Every route that stamps a chat onto an escalation, a peer query, an issue, a
// pipeline or an exposed port used to read s.ipc.ChatID — the chat of whichever
// run happened to start the sidecar. The crew shares one sidecar, so every such
// record from every agent in the container carried that one chat, regardless of
// who raised it or from where. assignment.go already said so in a comment; this
// is the fix for it.
//
// Degrades to the boot chat, not to empty: a v1 token carries no run, and a
// record with no chat at all is worse than one with the old, imprecise chat.
func (s *Server) requestChatID(r *http.Request) string {
	_, _, runID, _, _ := s.actingRunIdentity(r)
	return s.runChatID(runID)
}

// runChatID is the chat a call belongs to, resolved per REQUEST rather than
// read off the boot-frozen s.ipc.ChatID.
//
// s.ipc.ChatID is whichever chat happened to start this sidecar. The crew
// shares one sidecar, so every escalation, peer query, issue and exposed port
// raised by ANY agent in the container was stamped with that one chat — a fact
// the tree already acknowledged in assignment.go ("the crew shares one sidecar
// and its IPC chat is the boot agent's") without being able to do anything
// about it. A per-run token can: the run registry knows the chat the run was
// dispatched for.
//
// Falls back to the boot chat when the run is unknown, which keeps a v1 token
// and a pre-E0 sidecar behaving exactly as before rather than losing the chat
// reference entirely.
func (s *Server) runChatID(runID string) string {
	if st, known := s.runs.lookup(runID); known && st.ChatID != "" {
		return st.ChatID
	}
	if s.ipc != nil {
		return s.ipc.ChatID
	}
	return ""
}

// llmRouteIdentity extracts the per-agent token embedded in the disposable
// provider key. The real provider credential is injected only after this
// lookup, so these slots contain no upstream secret at this point. Supporting
// every auth shape is necessary: Anthropic uses x-api-key, OpenAI/OpenRouter
// use Authorization, and Gemini may use either its header or ?key=.
func (s *Server) llmRouteIdentity(r *http.Request) (agentID, configFingerprint string, present, ok bool) {
	values := []string{
		bearerToken(r),
		r.Header.Get("x-api-key"),
		r.Header.Get("x-goog-api-key"),
		r.URL.Query().Get("key"),
	}
	marker := internaltoken.LLMRoutePrefix + "."
	for _, value := range values {
		idx := strings.Index(value, marker)
		if idx < 0 {
			continue
		}
		routeIdentity := value[idx:]
		token, fp, hasFP := strings.Cut(routeIdentity, internaltoken.RouteFingerprintDelimiter)
		if !hasFP || fp == "" {
			return "", "", true, false
		}
		if s.routeAuth == nil {
			return "", fp, true, false
		}
		id, matched := internaltoken.ValidateLLMRouteToken(s.routeAuth.Key, token)
		return id, fp, true, matched
	}
	return "", "", false, false
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
