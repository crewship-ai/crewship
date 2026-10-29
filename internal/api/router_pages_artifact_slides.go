package api

import "net/http"

func (r *Router) registerArtifactSlidesRoutes() {
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// openapi: responses 200,400,401,403,404,413,422,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/slides", authed(wsCtx(http.HandlerFunc(r.pages.GetArtifactSlides))))
	// openapi: responses 200,400,401,403,404,413,422,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/artifact/export.pdf", authed(wsCtx(http.HandlerFunc(r.pages.ExportArtifactSlidesPDF))))
}
