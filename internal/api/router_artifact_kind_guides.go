package api

import "net/http"

// registerArtifactKindGuideRoutes serves the static embedded per-kind agent
// guides (internal/artifacts/kindguide). They read no tenant rows.
func (r *Router) registerArtifactKindGuideRoutes() {
	authed := r.authMw.RequireAuth
	// openapi: responses 200,401
	r.mux.Handle("GET /api/v1/artifact-kind-guides", authed(http.HandlerFunc(ListArtifactKindGuides)))
	// openapi: responses 200,401,404
	r.mux.Handle("GET /api/v1/artifact-kind-guides/{kind}", authed(http.HandlerFunc(GetArtifactKindGuide)))
}
