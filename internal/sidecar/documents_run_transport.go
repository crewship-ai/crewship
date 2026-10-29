package sidecar

import (
	"errors"
	"net/http"
	"strings"
)

// Document host routes derive the agent/run from the verified bearer and crew
// transport, then recheck the retained human principal. A startup roster slug
// is only descriptive: agents added after container startup may not have one.
// Never substitute the boot identity or copy a caller-supplied acting header.
func (s *Server) documentHostRequest(r *http.Request, method, path string, body []byte, responseLimit int64) (*memoryHostResponse, error) {
	if !((path == "/api/v1/internal/documents/create" && method == http.MethodPost) ||
		(path == "/api/v1/internal/documents/draft" && (method == http.MethodGet || method == http.MethodPost))) {
		return nil, &memoryHostTransportError{err: errors.New("unsupported Document transport")}
	}
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		return nil, &memoryHostTransportError{err: errors.New("live run capability required")}
	}
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !valid {
		return nil, &memoryHostTransportError{err: errors.New("live run capability refused")}
	}
	return s.sendBoundedHostRequest(r, method, path, body, responseLimit, slug)
}
