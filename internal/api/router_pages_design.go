package api

import "net/http"

func (r *Router) registerDesignRoutes() {
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// openapi: responses 201,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/design", roleInline, r.pages.CreateDesign)
	// openapi: responses 200,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/design", authed(wsCtx(http.HandlerFunc(r.pages.GetDesign))))
	// openapi: responses 200,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/design/versions", authed(wsCtx(http.HandlerFunc(r.pages.DesignVersions))))
	// openapi: responses 101,400,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/design/ws", authed(wsCtx(http.HandlerFunc(r.pages.DesignWebsocket))))
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/design/operations", roleInline, r.pages.ApplyDesign)
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/design/snapshots", roleInline, r.pages.SnapshotDesign)
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/design/restore", roleInline, r.pages.RestoreDesign)
	// openapi: responses 200,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/design/comments", authed(wsCtx(http.HandlerFunc(r.pages.GetDesignComments))))
	// openapi: responses 201,400,401,403,404,409,500
	r.authedMut("POST", "/api/v1/pages/{slug}/design/comments", roleInline, r.pages.CreateDesignComment)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedMut("POST", "/api/v1/pages/{slug}/design/share", roleInline, r.pages.ShareDesign)
	// openapi: responses 200,400,401,403,404,500
	r.authedMut("POST", "/api/v1/pages/{slug}/design/agent", roleInline, r.pages.ConnectDesignAgent)
}
