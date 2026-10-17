package sidecar

import (
	"net/http"
	"net/url"
	"strings"
)

// These read tools expose only the host's current run-scoped projection.
func (s *Server) handleArtifactToolRead(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		http.Error(w, "run capability required", 403)
		return
	}
	if _, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token); !present || !valid {
		http.Error(w, "run capability refused", 403)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	path := "/api/v1/internal/artifacts"
	if r.URL.Path != "/artifacts" {
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/artifacts/"), "/diff")
		if slug == "" || strings.ContainsAny(slug, "/\\") {
			http.Error(w, "invalid artifact slug", 400)
			return
		}
		path += "/" + url.PathEscape(slug) + "/diff"
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	response, err := s.boundedHostRequest(r, http.MethodGet, path, nil, 8<<20)
	if err != nil {
		http.Error(w, "artifact read unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if response.retryAfter != "" {
		w.Header().Set("Retry-After", response.retryAfter)
	}
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
