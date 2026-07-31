package api

// Internal sidecar route for LLM-driven skill generation
// (PRD-SLASH-CAPABILITIES-2026 §6.4).
//
// Mirrors internal_routines.go in shape. The internal entry takes
// workspace_id from the query (per the sidecar proxyToAPI convention)
// and puts it in the request CONTEXT, which is where
// SkillGenerateHandler.Generate reads it from.
//
// It used to read r.PathValue("workspaceId") instead, and this adapter
// stamped the value onto the path to suit it. That read was the
// path/query divergence hole: on the public route the middleware
// validates membership against ?workspace_id= while the path can name
// a different tenant (see the comment on Generate). The context value
// is now the only one that carries the workspace here — the
// SetPathValue below is kept for any middleware that logs the path
// variable, and is NOT what makes this route work.
//
// Same MANAGER role injection as internal_hire.go / internal_routines.go:
// the public Generate handler runs requireRole("create"); injecting
// MANAGER clears that pre-existing gate without making the sidecar
// claim more than it needs. The CAPABILITY gate added in commit 6 is
// the slash-action security boundary.

import (
	"context"
	"net/http"
)

// SkillInternalAdapter wraps SkillGenerateHandler.Generate so the
// internal /api/v1/internal/skills/generate route can dispatch into
// it with workspace + role context injected from query params.
type SkillInternalAdapter struct {
	gen *SkillGenerateHandler
}

// NewSkillInternalAdapter constructs the adapter at router-wiring
// time so it reuses the public SkillGenerateHandler instance
// (shared *sql.DB, shared logger, no duplicate state).
func NewSkillInternalAdapter(gen *SkillGenerateHandler) *SkillInternalAdapter {
	return &SkillInternalAdapter{gen: gen}
}

// Generate reads workspace_id from the query, sets it as the
// {workspaceId} path value the public handler expects, injects
// MANAGER role into the context, then calls the public Generate
// path. LLM call, Anthropic credential resolve, body scan, and
// skill upsert all run in the public handler unchanged.
//
// Dual-path: when X-Caller-User-Id is present (user-initiated
// slash command from chat-bridge / CLI repl), gates on
// skill.create capability before the LLM bill fires. Autonomous-
// agent path falls through — the autonomy gate runs upstream
// before this surface is hit.
func (h *SkillInternalAdapter) Generate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.gen == nil {
		replyError(w, http.StatusInternalServerError, "skill adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	callerID := CallerUserIDFromRequest(r)
	if callerID != "" {
		if !requireCapabilityOrForbid(w, r, h.gen.logger, h.gen.db,
			wsID, callerID,
			CapabilitySkillCreate, "skill.create", "skill:new") {
			return
		}
	}

	// Cosmetic: the internal route has no {workspaceId} pattern, so
	// anything that reads the path variable (request logging) sees the
	// same tenant the context carries. Generate does NOT read this —
	// deleting this line changes nothing, deleting the ctxWorkspaceID
	// value below breaks the route.
	r.SetPathValue("workspaceId", wsID)

	// This is the load-bearing line: Generate resolves the workspace
	// from the context. Pinned by
	// TestSkillAdapter_Internal_CarriesWorkspaceInContext.
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	if callerID != "" {
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: callerID, Email: "x-internal"})
	}
	r = r.WithContext(ctx)
	h.gen.Generate(w, r)
}
