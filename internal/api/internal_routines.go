package api

// Internal sidecar route for pipeline-schedule (routine) creation
// (PRD-SLASH-CAPABILITIES-2026 §6.4).
//
// Pattern mirror of internal_hire.go: the sidecar's /routines/schedules/
// create endpoint forwards here over X-Internal-Token; we inject the
// workspace + a MANAGER-tier role into the request context so the
// public PipelineHandler.CreateSchedule path runs unchanged.
//
// The role injection is the same belt-and-braces hack the hire adapter
// uses — it lets the sidecar-vouched call satisfy the public handler's
// canRole("create") gate without the sidecar binary needing to know
// the caller's actual workspace role. The CAPABILITY gate is the real
// security boundary for slash-initiated routine creation; that gate
// fires in commit 6's dual-path slash-action handler. The role check
// in CreateSchedule degrades to a no-op safety net once the capability
// path is the authoritative one (graduation milestone, post-rollout).
//
// Why route through the public handler instead of duplicating logic:
// CreateSchedule does cron-expression validation, timezone parsing,
// pipeline-slug→id resolution, audit emit, and SaveScheduleInput
// shaping. Forking that for the internal entry would drift over time.
// One handler, two surfaces (public + internal) keeps decisions
// consistent.

import (
	"context"
	"net/http"
)

// RoutineInternalAdapter wraps PipelineHandler.CreateSchedule so the
// internal /api/v1/internal/routines/schedules route can dispatch
// into it with workspace + role context injected from query params
// and the internal-token vouch.
type RoutineInternalAdapter struct {
	pipes *PipelineHandler
}

// NewRoutineInternalAdapter returns a wrapper that satisfies the
// http.HandlerFunc shape expected by router_internal.go's
// internalAuth wrapping. Construction at router-wiring time keeps
// the adapter dependency-free (it reuses the PipelineHandler the
// public router already instantiated).
func NewRoutineInternalAdapter(pipes *PipelineHandler) *RoutineInternalAdapter {
	return &RoutineInternalAdapter{pipes: pipes}
}

// CreateSchedule reads workspace_id from the query (the sidecar
// attaches it via proxyToAPI), injects MANAGER role into the
// context, then calls the public PipelineHandler.CreateSchedule
// path. The cron parse, timezone validate, audit emit, and store
// write all fire in the public handler unchanged.
//
// We use MANAGER (the lowest tier the public CreateSchedule handler
// accepts via canRole("create")) rather than OWNER for the same
// reason internal_hire.go uses MANAGER: a future per-action audit
// gate that splits OWNER from MANAGER shouldn't silently grant the
// sidecar admin-equivalent privileges. The CAPABILITY gate in the
// dual-path slash-action handler (commit 6) is the actual security
// boundary; this role injection just clears the existing role check
// that pre-dates capabilities.
func (h *RoutineInternalAdapter) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.pipes == nil {
		replyError(w, http.StatusInternalServerError, "routine adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	// Inject workspace + role context. Caller-identity propagation
	// (X-Caller-User-Id) flows through the headers untouched — the
	// dual-path handler in commit 6 reads it via
	// CallerUserIDFromRequest to choose capability vs autonomy gate.
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	r = r.WithContext(ctx)
	h.pipes.CreateSchedule(w, r)
}
