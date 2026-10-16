package sidecar

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var documentBlockID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// Document forwarding preserves the original run bearer. The Go authority
// revalidates the live run, human delegation and Page grant on every call.
func (s *Server) handleDocument(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	_, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid {
		http.Error(w, "run capability required", 403)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/pages/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || strings.ContainsAny(parts[0], "\\") || parts[1] != "document" {
		http.Error(w, "invalid document path", 400)
		return
	}
	allowed := r.Method == http.MethodGet && len(parts) == 2
	if r.Method == http.MethodPost && len(parts) == 3 && (parts[2] == "patch" || parts[2] == "checkpoint") {
		allowed = true
	}
	if !allowed {
		http.Error(w, "document operation unavailable to agent", 403)
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	block := ""
	if queryErr != nil {
		http.Error(w, "invalid document query", 400)
		return
	}
	if len(query) != 0 {
		for name, values := range query {
			if len(values) != 1 {
				http.Error(w, "duplicate document query", 400)
				return
			}
			switch name {
			case "block_id":
				if r.Method != http.MethodGet || len(parts) != 2 || !documentBlockID.MatchString(values[0]) {
					http.Error(w, "one valid block_id required", 400)
					return
				}
				block = values[0]
			case "workspace_id":
				if s.ipc == nil || values[0] != s.ipc.WorkspaceID {
					http.Error(w, "document workspace must match run context", 400)
					return
				}
			default:
				http.Error(w, "unsupported document query", 400)
				return
			}
		}
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	var body []byte
	if r.Method == http.MethodPost {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			http.Error(w, "document request too large", 413)
			return
		}
	}
	hostPath := "/api/v1/internal/documents/" + parts[0]
	if len(parts) == 3 {
		hostPath += "/" + parts[2]
	}
	if block != "" {
		hostPath += "?" + url.Values{"block_id": {block}}.Encode()
	}
	response, err := s.boundedHostRequest(r, r.Method, hostPath, body, 4<<20)
	if err != nil {
		http.Error(w, "document service unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
