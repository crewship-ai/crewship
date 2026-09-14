package api

// Routes for the durable work ledger — the work items an operator can see and
// act on, and the webhook deliveries that produced them
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
//
// Everything is nested under /api/v1/workspaces/{workspaceId}/… on purpose,
// and not only because scopeForRoute would fail the build on a new top-level
// segment: the ledger has no meaning outside a workspace, and a top-level
// /work-items would be one path parameter away from a cross-tenant read.
//
// Note the {workspaceId} spelling. RequireWorkspace reads
// r.PathValue("workspaceId") literally (internal/api/middleware.go), so
// {workspaceID} — which is how §9 of the PRD writes it — resolves to nothing
// and every route under it answers 400.

import "net/http"

func (r *Router) registerWorkRoutes() {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	workItems := NewWorkItemsHandler(r.db, r.logger)
	deliveries := NewWebhookDeliveriesHandler(r.db, r.logger)

	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/work-items", authed(wsCtx(http.HandlerFunc(workItems.List))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/work-items/{workItemId}", authed(wsCtx(http.HandlerFunc(workItems.Get))))

	// roleCreate (MANAGER+), not roleManage. Stopping a run and re-running one
	// are the same class of decision as starting one — POST .../pipelines/
	// {slug}/run is roleCreate — and an operator who may dispatch work but may
	// not stop it is an operator who has to escalate to an owner to end a
	// runaway agent.
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/work-items/{workItemId}/cancel", roleCreate, workItems.Cancel)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/work-items/{workItemId}/replay", roleCreate, workItems.Replay)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/work-items/{workItemId}/resolve", roleCreate, workItems.Resolve)

	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/webhook-deliveries", authed(wsCtx(http.HandlerFunc(deliveries.List))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/webhook-deliveries/{deliveryId}", authed(wsCtx(http.HandlerFunc(deliveries.Get))))
}
