package sidecar

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) handleBoardRequest(w http.ResponseWriter, r *http.Request) {
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
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/board-requests/"), "/")
	if len(parts) < 1 || len(parts) > 3 || parts[0] == "" {
		http.Error(w, "invalid job path", 400)
		return
	}
	path := "/api/v1/internal/board-requests/" + url.PathEscape(parts[0])
	limit := int64(2 << 20)
	if len(parts) == 2 && parts[1] == "mermaid" && r.Method == http.MethodPost {
		path += "/mermaid"
	} else if len(parts) == 3 && (parts[1] == "files" || parts[1] == "sources") && r.Method == http.MethodGet && parts[2] != "" {
		path += "/" + parts[1] + "/" + url.PathEscape(parts[2])
		limit = 64 << 20
	} else if len(parts) != 1 || r.Method != http.MethodGet {
		http.Error(w, "invalid job operation", 400)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil || len(data) > 65536 {
		http.Error(w, "job request too large", 413)
		return
	}
	response, err := s.boundedHostRequest(r, r.Method, path, data, limit)
	if err != nil {
		http.Error(w, "job outcome unavailable; retry same mutation_id", 503)
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
