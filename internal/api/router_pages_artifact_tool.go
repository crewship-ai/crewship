package api

import "net/http"

func (r *Router) registerArtifactProjectToolRoutes(internalAuth func(http.Handler) http.Handler) {
	// Both layers are required: crew IPC authenticates transport; the signed run
	// bearer and retained consent establish the exact human and Page mandate.
	r.mux.Handle("POST /api/v1/internal/pages/artifact-tool", internalAuth(http.HandlerFunc(r.pages.ArtifactProjectTool)))
}
