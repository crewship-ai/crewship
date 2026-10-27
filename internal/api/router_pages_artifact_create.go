package api

import "net/http"

func (r *Router) registerArtifactProjectCreateRoutes() {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// A read-scoped owner can discover availability; creation retains the
	// handler's current capability and exact original-principal checks.
	// openapi: responses 200,401,403,500
	r.mux.Handle("GET /api/v1/pages/artifacts/availability", authed(wsCtx(http.HandlerFunc(r.pages.ArtifactProjectAvailability))))
	// openapi: responses 201,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/artifacts", roleInline, r.pages.CreateArtifactProject)
}
