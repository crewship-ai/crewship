package sidecar

import (
	"io"
	"net/http"
	"strings"
)

// Creation has its own collection route; the host verifies the default-off
// human opt-in. No body field can nominate an owner, agent or run.
func (s *Server) handleArtifactCreate(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		http.Error(w, "run capability required", 403)
		return
	}
	if _, _, _, present, ok := s.identityForRunTokenCtx(r.Context(), token); !present || !ok {
		http.Error(w, "run capability refused", 403)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	data, err := io.ReadAll(io.LimitReader(r.Body, 60*1024*1024+4097))
	if err != nil || len(data) > 60*1024*1024+4096 {
		http.Error(w, "artifact request too large", 413)
		return
	}
	response, err := s.boundedHostRequest(r, http.MethodPost, "/api/v1/internal/artifacts", data, 64*1024)
	if err != nil {
		http.Error(w, "artifact creation outcome unavailable; retry the same mutation_id", 503)
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
