package api

import "net/http"

func (r *Router) registerArtifactProjectVersionRoutes() {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// openapi: responses 200,400,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact", authed(wsCtx(http.HandlerFunc(r.pages.GetArtifactProject))))
	// openapi: responses 200,400,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/versions", authed(wsCtx(http.HandlerFunc(r.pages.GetArtifactProject))))
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/versions", roleInline, r.pages.PublishArtifactProject)
}
