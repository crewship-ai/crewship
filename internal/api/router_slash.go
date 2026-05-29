package api

// Slash-command and capability-admin routes
// (PRD-SLASH-CAPABILITIES-2026 §6.6 + §6.8).
//
// Lives in its own registrar so the lifecycle is clean: GET
// /slash-commands (this commit) and the admin member/capabilities
// admin routes (commit 8) both register here. registerRoutes calls
// it after registerCrewsRoutes — capabilities table exists and the
// router has the DB handle by then.

import "net/http"

// registerSlashRoutes wires GET /api/v1/slash-commands. Admin
// capability-management routes land alongside in commit 8.
func (r *Router) registerSlashRoutes() {
	handler := NewSlashCommandsHandler(r)
	// Middleware aliases follow the registerCrewsRoutes convention
	// — same accessor names so a search across registrars finds
	// every route consistently. JWT-authed; user identity comes
	// from AuthMiddleware. wsCtx resolves workspace_id from
	// query/header so the handler can filter the catalog against
	// the right membership row.
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	r.mux.Handle("GET /api/v1/slash-commands",
		authed(wsCtx(http.HandlerFunc(handler.List))))
}
