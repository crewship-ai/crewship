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
		writeJSON(w, 200, DocumentAvailability{Enabled: p.documentCall != nil})
	}))))
	// openapi: responses 200,401,403,404,500,503
	r.mux.Handle("GET /api/v1/chats/{chatId}/document-results", authed(wsCtx(http.HandlerFunc(p.ListChatDocumentResults))))
	// openapi: responses 200,400,401,403,404,409,413,422,500,502,503
	r.mux.Handle("GET /api/v1/pages/{slug}/document", authed(wsCtx(http.HandlerFunc(p.GetDocument))))
	// openapi: responses 200,400,401,403,404,409,429,500,502,503
	r.mux.Handle("GET /api/v1/pages/{slug}/document/checkpoints", authed(wsCtx(http.HandlerFunc(p.ListDocumentCheckpoints))))
	// openapi: responses 200,400,401,403,404,409,429,500,502,503
	r.mux.Handle("GET /api/v1/pages/{slug}/document/checkpoints/{checkpoint}", authed(wsCtx(http.HandlerFunc(p.ReadDocumentCheckpoint))))
	// The private authority retains the ownership gate; inline also records
	// the CLI resource scope without adding a workspace role floor.
	// openapi: responses 200,400,401,403,404,409,413,422,429,500,502,503
	r.authedMut("POST", "/api/v1/pages/{slug}/document/initialize", roleInline, p.MutateDocument)
	// openapi: responses 200,400,401,403,404,409,413,422,429,500,502,503
	r.authedMut("POST", "/api/v1/pages/{slug}/document/patch", roleInline, p.MutateDocument)
}
