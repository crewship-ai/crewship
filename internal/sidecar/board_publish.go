package sidecar

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Boards use the same actual per-run bearer as artifact publication. The host
// authorizes the current human delegation and each affected element atomically.
func (s *Server) handleBoardPublish(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		http.Error(w, "run capability required", 403)
		return
	}
	_, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !valid {
		http.Error(w, "run capability refused", 403)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	slug := strings.TrimPrefix(r.URL.Path, "/boards/")
	if r.Method == http.MethodPost {
		slug = strings.TrimSuffix(slug, "/batches")
	}
	if slug == "" || strings.ContainsAny(slug, "/\\") {
		http.Error(w, "invalid board slug", 400)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 {
		http.Error(w, "board request too large", 413)
		return
	}
	path := "/api/v1/internal/boards/" + url.PathEscape(slug)
	if r.Method == http.MethodPost {
		path += "/batches"
	}
	response, err := s.boundedHostRequest(r, r.Method, path, data, 8*1024*1024)
	if err != nil {
		http.Error(w, "board outcome unavailable; retry the same mutation_id", 503)
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
