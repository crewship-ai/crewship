package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/slides"
)

func (r *Router) registerSlideRoutes() {
	h := &slideHTTP{pages: r.pages, store: slides.New(r.db, slides.Options{OnRevision: r.pages.pinSlideAssets})}
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	r.mux.Handle("GET /api/v1/pages/{slug}/slides", authed(wsCtx(http.HandlerFunc(h.Read))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/outline", authed(wsCtx(http.HandlerFunc(h.Outline))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/history", authed(wsCtx(http.HandlerFunc(h.History))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/revisions/{revision}", authed(wsCtx(http.HandlerFunc(h.Read))))
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/initialize", roleSelf, h.Mutate)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/patch", roleSelf, h.Mutate)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/reorder", roleSelf, h.Mutate)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/restore", roleSelf, h.Mutate)
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/comments", authed(wsCtx(http.HandlerFunc(h.ReadComments))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/comments/{threadId}", authed(wsCtx(http.HandlerFunc(h.ReadComments))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/comments/{threadId}/history", authed(wsCtx(http.HandlerFunc(h.ReadComments))))
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/comments", roleSelf, h.MutateComment)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/comments/{threadId}/replies", roleSelf, h.MutateComment)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/comments/{threadId}/resolve", roleSelf, h.MutateComment)

}
