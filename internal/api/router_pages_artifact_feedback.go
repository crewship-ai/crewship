package api

import "net/http"

func (r *Router) registerArtifactProjectFeedbackRoutes() {
	p := r.pages
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// Reads keep the same pinned snapshot authority as the project view.
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/comments", authed(wsCtx(http.HandlerFunc(p.GetArtifactProjectComments))))
	// Each mutation rechecks live credentials and Page ownership inside the
	// existing writer fence; workspace membership alone grants no write.
	// openapi: responses 200,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/restore", roleInline, p.RestoreArtifactProject)
	// openapi: responses 200,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/share", roleInline, p.ShareArtifactProject)
	// Library publication is owner only and separate from edit; a consumer's
	// pin is a self-contained copy that its readers read without the library.
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/library", authed(wsCtx(http.HandlerFunc(p.GetArtifactLibrary))))
	// openapi: responses 200,400,401,403,404,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/library/publish", roleInline, p.PublishArtifactLibrary)
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/library-pin", authed(wsCtx(http.HandlerFunc(p.GetArtifactLibraryPin))))
	// openapi: responses 200,400,401,403,404,409,413,500
	r.authedMut("PUT", "/api/v1/pages/{slug}/artifact/library-pin", roleInline, p.PutArtifactLibraryPin)
	// openapi: responses 200,400,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/library-pin/update", authed(wsCtx(http.HandlerFunc(p.GetArtifactLibraryPinUpdate))))
	// openapi: responses 201,400,401,403,404,409,413,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/comments", roleInline, p.CreateArtifactProjectComment)
	// openapi: responses 200,400,401,403,404,409,500
	r.authedMut("POST", "/api/v1/pages/{slug}/artifact/comments/{commentId}/reply", roleInline, p.ReplyArtifactProjectComment)
}
