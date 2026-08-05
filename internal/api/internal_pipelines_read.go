package api

// The two READ tools an in-container agent needs before it can author
// anything: what routines exist, and what its crew can reach.
//
// They used to forward to the PUBLIC, JWT-authed routes while the
// sidecar carries only X-Internal-Token — a header extractToken never
// looks at — so both answered 401 (#1763). An agent asked to write a
// routine could see neither, and wrote from memory instead. The
// capabilities dump exists to prevent exactly that.
//
// save_routine and run_routine were never affected because they already
// target /api/v1/internal/*. This adds the missing counterparts for the
// read side, following internal_routines.go: inject the workspace (and,
// for the list, a role) into the context and delegate to the public
// handler, so filters, isolation checks and response shapes come from
// one implementation rather than a fork that drifts.
//
// Deliberately NOT solved by teaching extractToken about
// X-Internal-Token: that would hand every holder of the shared internal
// secret a user-equivalent session across the entire public API, which
// is a far larger decision than two tools being unreachable.
// TestPublicPipelineRoutes_RejectTheInternalToken pins that.

import (
	"context"
	"net/http"
)

// PipelineReadInternalAdapter delegates the internal read routes into
// the public handlers the dashboard and CLI already use.
type PipelineReadInternalAdapter struct {
	pipes *PipelineHandler
	crews *CrewHandler
}

// NewPipelineReadInternalAdapter wires the adapter at router-build time
// from the handlers the public router already instantiated.
func NewPipelineReadInternalAdapter(pipes *PipelineHandler, crews *CrewHandler) *PipelineReadInternalAdapter {
	return &PipelineReadInternalAdapter{pipes: pipes, crews: crews}
}

// ListPipelines serves GET /api/v1/internal/pipelines?workspace_id=…
//
// The workspace is required rather than defaulted. Defaulting it would
// make a malformed call return an empty list, which reads to a model as
// "this workspace has no routines" — a confident wrong answer, and the
// worst of the available failures.
func (h *PipelineReadInternalAdapter) ListPipelines(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.pipes == nil {
		replyError(w, http.StatusInternalServerError, "pipeline read adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	// VIEWER: listing is a read, and the public List does not gate on
	// role. Injecting the lowest tier that satisfies any downstream
	// check keeps the sidecar from silently acquiring authority it does
	// not need — the same reasoning internal_routines.go gives for
	// choosing MANAGER over OWNER on the write path.
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "VIEWER")
	h.pipes.List(w, r.WithContext(ctx))
}

// CrewCapabilities serves
// GET /api/v1/internal/crews/{crewId}/capabilities?workspace_id=…
//
// The public handler reads crewId from the path and the workspace from
// the context, and does its own isolation check — a crew from another
// workspace is refused there, not here, so there is one place that
// decides it.
func (h *PipelineReadInternalAdapter) CrewCapabilities(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.crews == nil {
		replyError(w, http.StatusInternalServerError, "crew capabilities adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "VIEWER")
	h.crews.Capabilities(w, r.WithContext(ctx))
}
