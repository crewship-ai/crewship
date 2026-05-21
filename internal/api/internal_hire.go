package api

// Internal sidecar route for LEAD-driven ephemeral hire (PR-D F5).
//
// Wires the sidecar /spawn endpoint to the same AgentHandler.Hire
// pipeline the public API uses. The internal-token requireInternal
// middleware vouches that the request originated from a trusted
// sidecar; we inject the LEAD's effective workspace + role into the
// request context so the downstream RBAC + workspace scope checks
// pass without bypassing the policy gate (which is the actual
// security boundary for ephemeral spawn).
//
// Why we route through AgentHandler.Hire instead of duplicating the
// logic: the autonomy_level gate, quota check, audit log, and inbox
// emission are non-trivial. Forking them would drift over time.
// One handler, two entry surfaces (public + internal) keeps the
// policy decisions consistent regardless of who initiated the hire.

import (
	"context"
	"net/http"
)

// HireInternalAdapter wraps AgentHandler.Hire so the internal
// /api/v1/internal/agents/hire route can dispatch into it with
// workspace + role context injected from query params + internal
// elevation. Carried on Router so registerInternalRoutes can wire it
// without re-constructing the public AgentHandler.
type HireInternalAdapter struct {
	agents *AgentHandler
}

// NewHireInternalAdapter returns a wrapper that satisfies the
// http.HandlerFunc shape expected by router_internal.go's
// internalAuth wrapping.
func NewHireInternalAdapter(agents *AgentHandler) *HireInternalAdapter {
	return &HireInternalAdapter{agents: agents}
}

// Hire reads workspace_id from the query (the sidecar attaches it via
// proxyToAPI), injects MANAGER role into the context (sidecar-vouched
// requests are trusted at that tier per the PR-D F5 PRD), then calls
// the public AgentHandler.Hire path. The policy gate STILL fires —
// strict crews still reject; trusted/full crews still auto-log; etc.
func (h *HireInternalAdapter) Hire(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.agents == nil {
		replyError(w, http.StatusInternalServerError, "hire adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	// Inject workspace + role context. We use MANAGER (the lowest
	// tier the public Hire handler accepts) rather than OWNER so a
	// future audit trail or per-action gate that distinguishes the
	// two doesn't silently grant the sidecar admin-equivalent
	// privileges. The policy gate is the real security boundary for
	// ephemeral_spawn; RBAC is just "is this a write-tier caller".
	//
	// We do NOT inject a user_id — the audit log will record an
	// empty user_id, which the journal layer correctly classifies
	// as actor.system. PR-D.7 / Captain integration will inject the
	// LEAD's agent_id once we have a clean side-channel for that
	// (today the sidecar IPC config only carries workspace_id, not
	// the per-call lead identity).
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	r = r.WithContext(ctx)
	h.agents.Hire(w, r)
}
