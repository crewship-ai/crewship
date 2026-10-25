package api

import "net/http"

// The original ingress stamp precedes authentication and body decoding. Page
// policy is enforced by the controller with the exact verified source; this
// self-scoped declaration does not make workspace membership a content grant.
func (r *Router) registerMooringRoutes() {
	if !r.mooringHTTPConfig.Enabled {
		return
	}
	manager, err := newCollaborationHTTPManager(r.pages, r.mooringHTTPConfig)
	if err != nil {
		r.mooringStartupErr = err
		return
	}
	r.mooringHTTP = manager
	// A missing supported private runtime leaves the task endpoint unavailable;
	// it must not turn a human session or SQL work row into agent authority.
	_ = manager.startAgentTasks(r.webhookHandler)
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// Literal registrations keep the CLI and documentation route inventories
	// able to verify these paths and their workspace requirement.
	r.authedMut("POST", "/api/v1/pages/{slug}/mooring/session", roleSelf, manager.session, manager.fence.Wrap)
	r.authedMut("POST", "/api/v1/pages/{slug}/mooring/tasks", roleSelf, manager.task, manager.fence.Wrap)
	r.mux.Handle("GET /api/v1/pages/{slug}/mooring/ws", manager.fence.Wrap(authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(manager.websocket))))))
}
