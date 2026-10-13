package sidecar

import (
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) handleArtifactRequestContext(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	_, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid {
		http.Error(w, "run capability required", 403)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	id := strings.TrimPrefix(r.URL.Path, "/artifact-requests/")
	if id == "" || strings.ContainsAny(id, "/\\") {
		http.Error(w, "invalid request id", 400)
		return
	}
	response, err := s.boundedHostRequest(r, http.MethodGet, "/api/v1/internal/artifact-requests/"+url.PathEscape(id), nil, 2<<20)
	if err != nil {
		http.Error(w, "request context unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
