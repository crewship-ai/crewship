package sidecar

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// issue_attachments.go — the attachment verbs of the agent's issue surface
// (#1768, item 7).
//
//	GET  /issue/{identifier}/attachments        list what is attached
//	GET  /issue/{identifier}/attachments/{id}   read one, fenced and budgeted
//	POST /issue/{identifier}/attachments        attach a file the agent produced
//
// Same shape as every other verb in issue_verbs.go, and for the same reasons:
// the ACTING agent comes from the per-agent bearer token and never from the
// body, the workspace comes from the trusted IPC identity, and the crew is not
// sent at all. Reads resolve identity too — an attachment is crew data, and a
// sibling that drops its header must not read it as the boot agent.
//
// Nothing here fences anything. That is deliberate and is not an omission: the
// backend handler (internal/api/issue_attachments_internal.go) wraps both the
// filename and the content before they leave crewshipd, so the fence sits at the
// point that knows which bytes are text and which are base64. Doing it a second
// time here would double-wrap the text and would wrap the base64 that must stay
// raw. The one thing this file must not do is UNDO it, which is why the reads
// below are plain forwards with no transform.

// attachmentUploadBodyBytes caps the JSON body of an agent attach call.
//
// The control-plane cap (1 MiB) is far too small: the file rides as base64,
// which costs 4 bytes for every 3, so a 6 MiB file — the backend's own agent
// ceiling — needs ~8 MiB of body plus the JSON envelope. This is that number,
// and it is still well under the pipeline data-plane cap.
//
// It bounds the SIDECAR's exposure; the backend enforces its own decoded-size
// limit independently, because a cap that only exists in the proxy is a cap that
// disappears the moment anything else holds the internal token.
const attachmentUploadBodyBytes = 9 << 20

// issueAttachmentPath splits /issue/<IDENT>/attachments[/<ID>] into its parts.
//
// The identifier and the attachment id are both concatenated into the internal
// IPC URL, so both are checked against issueIdentInjectionChars for the reason
// #1045 documents: a percent-encoded `?` or `&` would smuggle query parameters
// past the trusted scope query the caller appends. Returns ok=false after
// writing the 400.
func issueAttachmentPath(w http.ResponseWriter, path string) (ident, attachmentID string, ok bool) {
	rest := strings.TrimPrefix(path, "/issue/")
	ident, tail, found := strings.Cut(rest, "/attachments")
	if !found || ident == "" || strings.ContainsAny(ident, issueIdentInjectionChars) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "issue identifier required"})
		return "", "", false
	}
	attachmentID = strings.TrimPrefix(tail, "/")
	if attachmentID != "" && strings.ContainsAny(attachmentID, issueIdentInjectionChars+"/") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid attachment id"})
		return "", "", false
	}
	return ident, attachmentID, true
}

// handleIssueAttachmentsList handles GET /issue/{identifier}/attachments.
//
// Metadata only — filename, type, size, digest, who attached it. Reading the
// content is a second, explicit call, so an issue carrying four log files does
// not put all four into the agent's context whether or not it wanted any.
func (s *Server) handleIssueAttachmentsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.issueActor(w, r); !ok {
		return
	}
	ident, _, ok := issueAttachmentPath(w, r.URL.Path)
	if !ok {
		return
	}
	s.proxyToAPI(w, r, http.MethodGet,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+"/attachments"+s.issueScopeQuery())
}

// handleIssueAttachmentRead handles GET /issue/{identifier}/attachments/{id}.
//
// The response carries the content already fenced (text) or base64-encoded
// (everything else), plus a `truncated` flag when the budget bit. An agent that
// ignores that flag is reading a prefix and reasoning about a file it has only
// partly seen — which is why the flag is always present rather than omitted when
// false.
func (s *Server) handleIssueAttachmentRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.issueActor(w, r); !ok {
		return
	}
	ident, attachmentID, ok := issueAttachmentPath(w, r.URL.Path)
	if !ok {
		return
	}
	if attachmentID == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "attachment id required"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+"/attachments/"+url.PathEscape(attachmentID)+s.issueScopeQuery())
}

// handleIssueAttach handles POST /issue/{identifier}/attachments.
//
// Body: {"filename": "...", "content_base64": "..."}. The agent supplies bytes
// it produced — a generated report, a captured log, a diff it wants a human to
// look at — and the file becomes part of the issue's record rather than
// something buried in a chat transcript.
func (s *Server) handleIssueAttach(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.issueActor(w, r)
	if !ok {
		return
	}
	ident, _, ok := issueAttachmentPath(w, r.URL.Path)
	if !ok {
		return
	}

	var req struct {
		Filename      string `json:"filename"`
		ContentBase64 string `json:"content_base64"`
		// SECURITY: parsed and ignored. A crew shares one container and one
		// sidecar, so a request-supplied author would let any member attach a
		// file AS any other — the impersonation the per-agent token exists to
		// stop. Kept in the struct so the field reads as refused rather than
		// merely unknown. Same treatment as handleIssueComment.
		AgentID     string `json:"agent_id"`
		WorkspaceID string `json:"workspace_id"` // SECURITY: ignored — IPC identity wins.
	}
	if !decodeCappedJSONLimit(w, r, &req, attachmentUploadBodyBytes) {
		return
	}
	if strings.TrimSpace(req.Filename) == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "filename is required"})
		return
	}
	if req.ContentBase64 == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "content_base64 is required"})
		return
	}

	payload, err := json.Marshal(map[string]interface{}{
		"workspace_id":   s.ipc.WorkspaceID,
		"agent_id":       agentID,
		"filename":       req.Filename,
		"content_base64": req.ContentBase64,
	})
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPost,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+"/attachments", "issue attach", payload)
}
