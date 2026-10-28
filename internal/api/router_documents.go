package api

import (
	"context"
	"net"
	"net/http"
	"time"
)

func (r *Router) registerDocumentRoutes() {
	p := r.pages
	p.documentStoreOptions = r.documentStoreOptions
	if r.documentsServiceSocket != "" && r.documentsSocket != "" {
		r.documentsClient = &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", r.documentsServiceSocket)
		}, MaxIdleConns: 16, MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second}}
		p.documentCall = r.documentServiceCall
	}
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace
	// openapi: responses 200,400,401,403,500
	r.mux.Handle("GET /api/v1/documents/status", authed(wsCtx(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, r.documentAvailability(req))
	}))))
	// openapi: responses 200,401,403,404,500,503
	r.mux.Handle("GET /api/v1/chats/{chatId}/document-results", authed(wsCtx(http.HandlerFunc(p.ListChatDocumentResults))))
	// openapi: responses 200,400,401,403,404,409,413,422,500,502,503
	r.mux.Handle("GET /api/v1/pages/{slug}/document", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	// openapi: responses 200,400,401,403,404,409,429,500,502,503
	r.mux.Handle("GET /api/v1/pages/{slug}/document/checkpoints", authed(wsCtx(http.HandlerFunc(p.ListDocumentCheckpoints))))
	// openapi: responses 200,400,401,403,404,409,429,500,502,503
	r.mux.Handle("GET /api/v1/pages/{slug}/document/checkpoints/{checkpoint}", authed(wsCtx(http.HandlerFunc(p.ReadDocumentCheckpoint))))
	// openapi: responses 200,400,401,403,404,409,413,422,429,500,502,503
	r.authedMut("POST", "/api/v1/pages/{slug}/document/restore", roleInline, p.RestoreDocumentCheckpoint)
	// The private authority retains the ownership gate; inline also records
	// the CLI resource scope without adding a workspace role floor.
	// openapi: responses 200,400,401,403,404,409,413,422,429,500,502,503
	r.authedMut("POST", "/api/v1/pages/{slug}/document/initialize", roleInline, p.MutateDocument)
	// openapi: responses 200,400,401,403,404,409,413,422,429,500,502,503
	r.authedMut("POST", "/api/v1/pages/{slug}/document/patch", roleInline, p.MutateDocument)
}

// This advertises release assets only. Initialization and every later content
// operation continue through the existing Page and original-source authority.
func (r *Router) documentAvailability(req *http.Request) DocumentAvailability {
	status := DocumentAvailability{Enabled: r.pages.documentCall != nil}
	if d := r.craftNativeDocuments; d != nil && d.human != nil && d.human.enabled {
		_, status.WordCraftNative = d.applications["wordcraft"]
	}
	if m := r.mooringHTTP; m != nil && m.commandFactory != nil && m.commandFactory.editorBundle != nil {
		f := m.commandFactory
		f.mu.Lock()
		live := !f.closed && f.enabled
		f.mu.Unlock()
		if live {
			digest, indexURL := f.editorBundle(req)
			status.WordCraftCommands = commandDigestValid("sha256:"+digest) && indexURL != ""
		}
	}
	if m := r.mooringHTTP; m != nil && m.config.Enabled && m.config.EditorBundle != nil {
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if !closed {
			digest, indexURL := m.config.EditorBundle(req)
			if digest != "" && indexURL != "" {
				status.WordCraftSDKPrefix = m.config.SDKPrefix
			}
		}
	}
	return status
}
