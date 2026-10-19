package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// documentServiceCall carries the caller's credential to the independent engine.
// The engine calls the Go authority, which rechecks it under the SQL writer fence.
func (r *Router) documentServiceCall(ctx context.Context, workspace, bearer, page, operation string, body any) ([]byte, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 500, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docs/v1/documents/"+page+"/"+operation, bytes.NewReader(payload))
	if err != nil {
		return nil, 500, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	req.Header.Set("X-Workspace-ID", workspace)
	req.Header.Set("X-Crewship-Docs-Service", r.documentsServiceSecret)
	response, err := r.documentsClient.Do(req)
	if err != nil {
		return nil, 503, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, 502, io.ErrUnexpectedEOF
	}
	return raw, response.StatusCode, nil
}

func (h *PageHandler) documentPage(w http.ResponseWriter, r *http.Request) (*pageRecord, bool) {
	if h.documentCall == nil {
		replyError(w, 503, "Docs are not enabled on this server")
		return nil, false
	}
	rec, err := h.loadPage(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if err != nil {
		replyError(w, 404, "document page not found")
		return nil, false
	}
	return rec, true
}
func (h *PageHandler) GetDocument(w http.ResponseWriter, r *http.Request) {
	h.serveDocument(w, r, false)
}
func (h *PageHandler) MutateDocument(w http.ResponseWriter, r *http.Request) {
	h.serveDocument(w, r, true)
}
func (h *PageHandler) serveDocument(w http.ResponseWriter, r *http.Request, mutate bool) {
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "human document credentials required")
		return
	}
	if r.URL.Query().Has("block_id") {
		replyError(w, 400, "selected block reads are not enabled")
		return
	}
	rec, ok := h.documentPage(w, r)
	if !ok {
		return
	}
	if !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	operation := "read"
	var body any = map[string]any{}
	if mutate {
		operation = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if operation != "initialize" && operation != "patch" {
			replyError(w, 404, "unknown document operation")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		decoder := json.NewDecoder(r.Body)
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || decoder.Decode(new(any)) != io.EOF {
			replyError(w, 400, "one bounded JSON object required")
			return
		}
		object := bytes.TrimSpace(raw)
		if len(object) == 0 || object[0] != '{' {
			replyError(w, 400, "one bounded JSON object required")
			return
		}
		body = raw
	}
	bearer := "Bearer " + extractToken(r)
	raw, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), bearer, rec.ID, operation, body)
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		replyError(w, 503, "document engine unavailable; changes are not acknowledged")
		return
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		replyError(w, 502, "invalid document engine response")
		return
	}
	if operation == "read" {
		value["page_id"] = rec.ID
		user := UserFromContext(r.Context())
		value["can_write"] = user != nil && h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID, RoleFromContext(r.Context()), rec)
		value["can_comment"] = false
		value["can_resolve"] = false
	}
	writeJSON(w, 200, value)
}
