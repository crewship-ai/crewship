package sidecar

// Slash-action routes for credential Create + Rotate
// (PRD-SLASH-CAPABILITIES-2026 §6.5).
//
// Differs from routine_schedule.go / skill_generate.go in one
// material respect: the backend mirror (internal_credentials_mutate.go)
// requires X-Caller-User-Id and 401s without it. Autonomous-agent
// credential mutation is intentionally not supported — every
// rotation MUST name a human in the audit log. The sidecar still
// just forwards; the rejection lives at the backend so the rule
// is enforced in one place.
//
// Calling convention:
//
//	POST http://localhost:9119/credentials/create
//	Content-Type: application/json
//	X-Caller-User-Id: <user id>     (REQUIRED — backend 401s without)
//	X-Caller-Source:  chat-ui | cli-repl
//	{
//	  "name": "GitHub PAT (ci-bot)",
//	  "type": "SECRET",
//	  "value": "ghp_..."
//	}
//
//	POST http://localhost:9119/credentials/{credentialId}/rotate
//	Content-Type: application/json
//	X-Caller-User-Id: <user id>     (REQUIRED)
//	X-Caller-Source:  chat-ui | cli-repl
//	{
//	  "value": "ghp_new_..."
//	}

import (
	"net/http"
	"net/url"
	"strings"
)

// handleCredentialCreate proxies POST /credentials/create.
func (s *Server) handleCredentialCreate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/credentials?"+q.Encode())
}

// handleCredentialRotate proxies POST /credentials/{credentialId}/rotate.
// Extracts the credentialId segment from the inbound path and
// rebuilds it onto the internal path — proxyToAPI doesn't carry
// path params automatically (it takes a target path string), so the
// caller is responsible for re-stitching them.
func (s *Server) handleCredentialRotate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	// Path shape: /credentials/{credentialId}/rotate
	// Strip the leading /credentials/, then strip the trailing
	// /rotate. The result is the credentialId. Reject anything that
	// doesn't conform — a malformed path here would be a sidecar
	// caller bug worth a 400, not a 500.
	rest := strings.TrimPrefix(r.URL.Path, "/credentials/")
	credID := strings.TrimSuffix(rest, "/rotate")
	if credID == "" || credID == rest || strings.ContainsAny(credID, "/?#") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid credential path"})
		return
	}
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	// url.PathEscape on credID — defensive: credentialId is normally
	// a server-issued opaque token but a typo or hostile input could
	// carry reserved characters that break the downstream parse.
	s.proxyToAPI(w, r, http.MethodPost,
		"/api/v1/internal/credentials/"+url.PathEscape(credID)+"/rotate?"+q.Encode())
}
