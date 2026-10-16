package sidecar

import (
	"io"
	"net/http"
	"regexp"
	"strings"
)

var selectedPathSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (s *Server) handleSelectedSlides(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	_, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid {
		http.Error(w, "run capability required", 403)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/pages/"), "/")
	if len(parts) < 4 || len(parts) > 5 || !selectedPathSegment.MatchString(parts[0]) || parts[1] != "slides" || parts[2] != "requests" || !selectedPathSegment.MatchString(parts[3]) || r.URL.RawPath != "" || r.URL.RawQuery != "" {
		http.Error(w, "invalid selected Slides path", 400)
		return
	}
	if !(r.Method == http.MethodGet && len(parts) == 4) && !(r.Method == http.MethodPost && len(parts) == 5 && parts[4] == "replace") {
		http.Error(w, "Slides operation unavailable to agent", 403)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	var body []byte
	if r.Method == http.MethodPost {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
		if err != nil {
			http.Error(w, "selected replacement too large", 413)
			return
		}
	}
	hostPath := "/api/v1/internal/slides/" + parts[0] + "/requests/" + parts[3]
	if len(parts) == 5 {
		hostPath += "/replace"
	}
	// A valid32KiB original text can expand sixfold when JSON escapes '<',
	// plus the bounded quote/instruction and envelope. Keep response admission
	// separate from semantic text limits and replacement's256KiB wire budget.
	response, err := s.boundedHostRequest(r, r.Method, hostPath, body, 512<<10)
	if err != nil {
		http.Error(w, "selected Slides service unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
