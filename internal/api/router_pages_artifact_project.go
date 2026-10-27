package api

import "net/http"

func (r *Router) registerArtifactProjectRoutes() {
	p := r.pages
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// openapi: responses 200,401,403,500
	r.mux.Handle("GET /api/v1/pages/artifacts/availability", authed(wsCtx(http.HandlerFunc(p.ArtifactProjectAvailability))))
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact", authed(wsCtx(http.HandlerFunc(p.GetArtifactProject))))
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/versions", authed(wsCtx(http.HandlerFunc(p.GetArtifactProject))))
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/comments", authed(wsCtx(http.HandlerFunc(p.GetArtifactProjectComments))))
	// openapi: responses 201,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/artifacts", roleInline, p.CreateArtifactProject)
	// openapi: responses 200,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/versions", roleInline, p.PublishArtifactProject)
	// openapi: responses 200,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/restore", roleInline, p.RestoreArtifactProject)
	// openapi: responses 200,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/share", roleInline, p.ShareArtifactProject)
	// openapi: responses 201,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/comments", roleInline, p.CreateArtifactProjectComment)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/comments/{commentId}/reply", roleInline, p.ReplyArtifactProjectComment)
}
