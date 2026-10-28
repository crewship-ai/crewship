package api

import "net/http"

func (r *Router) registerArtifactDataRoutes() {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/data", authed(wsCtx(http.HandlerFunc(r.pages.ListArtifactData))))
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/data/{collection}", authed(wsCtx(http.HandlerFunc(r.pages.GetArtifactData))))
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/data/{collection}", roleInline, r.pages.BatchArtifactData)
}
