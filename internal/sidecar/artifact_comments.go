package sidecar

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) handleArtifactComments(w http.ResponseWriter, r *http.Request) {
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
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/artifacts/"), "/")
	if len(parts) != 2 && len(parts) != 4 {
		http.Error(w, "invalid discussion route", 400)
		return
	}
	if parts[0] == "" || parts[1] != "threads" {
		http.Error(w, "invalid discussion route", 400)
		return
	}
	path := "/api/v1/internal/artifacts/" + url.PathEscape(parts[0]) + "/threads"
	if len(parts) == 4 {
		if r.Method != http.MethodPost || parts[2] == "" || parts[3] != "comments" {
			http.Error(w, "reply operation required", 400)
			return
		}
		path += "/" + url.PathEscape(parts[2]) + "/comments"
	} else if r.Method != http.MethodGet {
		http.Error(w, "read operation required", 400)
		return
	}
	if r.Method == http.MethodGet && r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 26625))
	if err != nil || len(data) > 26624 {
		http.Error(w, "comment request too large", 413)
		return
	}
	response, err := s.boundedHostRequest(r, r.Method, path, data, 16<<20)
	if err != nil {
		http.Error(w, "discussion outcome unavailable; retry the same mutation_id", 503)
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
