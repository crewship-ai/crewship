package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
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

func (r *Router) registerDocumentRoutes() {
	p := r.pages
	if r.documentsServiceSocket != "" && r.documentsSocket != "" {
		r.documentsClient = &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", r.documentsServiceSocket)
		}, MaxIdleConns: 16, MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second}}
		p.documentCall = r.documentServiceCall
	}
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	r.mux.Handle("GET /api/v1/documents/status", authed(wsCtx(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, 200, DocumentAvailability{Enabled: p.documentCall != nil})
	}))))
	r.mux.Handle("GET /api/v1/pages/{slug}/document", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	r.mux.Handle("GET /api/v1/pages/{slug}/document/checkpoints", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	r.mux.Handle("GET /api/v1/pages/{slug}/document/comments", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	r.mux.Handle("GET /api/v1/pages/{slug}/document/export", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	r.mux.Handle("GET /api/v1/pages/{slug}/document/checkpoints/{checkpoint}", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	r.authedMut("POST", "/api/v1/pages/{slug}/document/initialize", roleSelf, p.MutateDocument)
	r.authedMut("POST", "/api/v1/pages/{slug}/document/patch", roleSelf, p.MutateDocument)
	r.authedMut("POST", "/api/v1/pages/{slug}/document/upgrade", roleSelf, p.MutateDocument)
	r.authedMut("POST", "/api/v1/pages/{slug}/document/checkpoint", roleSelf, p.MutateDocument)
	r.authedMut("POST", "/api/v1/pages/{slug}/document/restore", roleSelf, p.MutateDocument)
	r.authedMut("POST", "/api/v1/pages/{slug}/document/comments", roleSelf, p.MutateDocument)
	r.authedMut("POST", "/api/v1/pages/{slug}/document/comments/resolve", roleSelf, p.MutateDocument)
	r.mux.Handle("GET /api/v1/pages/{slug}/document/requests", authed(wsCtx(http.HandlerFunc(p.ListDocumentRequests))))
	r.authedMut("POST", "/api/v1/pages/{slug}/document/requests", roleSelf, p.CreateDocumentRequest)
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
	target, selected, selectorErr := documentBlockSelector(r)
	if selectorErr != nil {
		replyError(w, 400, selectorErr.Error())
		return
	}
	rec, ok := h.documentPage(w, r)
	if !ok {
		return
	}
	if selected {
		h.readDocumentBlock(w, r, rec, target)
		return
	}
	operation := "read"
	var body any = map[string]any{}
	if mutate {
		operation = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if operation == "comments" {
			operation = "comment"
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		decoder := json.NewDecoder(r.Body)
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || decoder.Decode(new(any)) != io.EOF {
			replyError(w, 400, "one bounded JSON object required")
			return
		}
		body = raw
	} else {
		if checkpoint := r.PathValue("checkpoint"); checkpoint != "" {
			operation = "checkpoint-read"
			body = map[string]any{"checkpoint": checkpoint}
		} else {
			last := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if last != "document" && !strings.HasPrefix(r.URL.Path, "/api/v1/internal/documents/") {
				operation = last
			}
		}
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
	if operation == "export" {
		var value struct {
			Markdown string `json:"markdown"`
		}
		if json.Unmarshal(raw, &value) != nil {
			replyError(w, 502, "invalid document engine response")
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=document.md")
		_, _ = io.WriteString(w, value.Markdown)
		return
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		replyError(w, 502, "invalid document engine response")
		return
	}
	if operation == "read" || operation == "comments" {
		value["page_id"] = rec.ID
		user := UserFromContext(r.Context())
		value["can_write"] = user != nil && h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID, RoleFromContext(r.Context()), rec)
		value["can_resolve"] = value["can_write"]
		value["can_comment"] = user != nil && h.mayComment(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID, RoleFromContext(r.Context()), rec)
	}
	writeJSON(w, 200, value)
}

// The sidecar authenticates its transport separately; the original run bearer
// still reaches Go's transactional document authority for every operation.
func (h *PageHandler) DocumentAgent(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "agtv2" {
		replyError(w, 403, "verified document run required")
		return
	}
	run, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		replyError(w, 403, "invalid run")
		return
	}
	var workspace string
	if h.db.QueryRowContext(r.Context(), `SELECT workspace_id FROM artifact_run_principals WHERE run_id=?`, string(run)).Scan(&workspace) != nil {
		replyError(w, 403, "unknown run")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, workspace))
	h.serveDocument(w, r, r.Method != "GET")
}
