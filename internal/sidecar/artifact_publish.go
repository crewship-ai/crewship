package sidecar

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

// There is no legacy/boot-identity fallback and no local publication fallback.
// The host independently validates the actual run bearer and live delegation.
func (s *Server) handleArtifactPublish(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		http.Error(w, "run capability required", 403)
		return
	}
	_, _, _, present, ok := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !ok {
		http.Error(w, "run capability refused", 403)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	action := "revisions"
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/restore") {
		action = "restore"
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/artifacts/"), "/"+action)
	if slug == "" || strings.ContainsAny(slug, "/\\") {
		http.Error(w, "invalid artifact slug", 400)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 60*1024*1024+4097))
	if err != nil || len(data) > 60*1024*1024+4096 {
		http.Error(w, "artifact request too large", 413)
		return
	}
	path := "/api/v1/internal/artifacts/" + url.PathEscape(slug) + "/" + action
	if r.Method == http.MethodGet {
		path = "/api/v1/internal/artifacts/" + url.PathEscape(slug)
		if rev := r.URL.Query().Get("revision"); rev != "" {
			path += "?revision=" + url.QueryEscape(rev)
		}
	}
	response, err := s.boundedHostRequest(r, r.Method, path, data, 64*1024*1024)
	if err != nil {
		http.Error(w, "artifact publication outcome unavailable; retry the same mutation_id", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if response.retryAfter != "" {
		w.Header().Set("Retry-After", response.retryAfter)
	}
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
