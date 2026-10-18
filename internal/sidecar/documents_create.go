package sidecar

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) handleDocumentCreate(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		http.Error(w, "live run capability required", 403)
		return
	}
	if _, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token); !present || !valid {
		http.Error(w, "live run capability refused", 403)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 1 {
		http.Error(w, "unsupported document creation query", 400)
		return
	}
	for name, values := range query {
		if name != "workspace_id" || len(values) != 1 || s.ipc == nil || values[0] != s.ipc.WorkspaceID {
			http.Error(w, "document workspace must match run context", 400)
			return
		}
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "document authoring exceeds 1 MiB", 413)
		return
	}
	response, err := s.boundedHostRequest(r, http.MethodPost, "/api/v1/internal/documents/create", body, 64<<10)
	if err != nil {
		http.Error(w, "document creation outcome unavailable; retry the same mutation_id", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
