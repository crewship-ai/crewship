package api

import (
	"github.com/crewship-ai/crewship/internal/slides"
	"net/http"
)

func (r *Router) registerSlideRoutes() {
	h := &slideHTTP{pages: r.pages, store: slides.New(r.db, slides.Options{})}
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	r.mux.Handle("GET /api/v1/pages/{slug}/slides", authed(wsCtx(http.HandlerFunc(h.Read))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/outline", authed(wsCtx(http.HandlerFunc(h.Outline))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/history", authed(wsCtx(http.HandlerFunc(h.History))))
	r.mux.Handle("GET /api/v1/pages/{slug}/slides/revisions/{revision}", authed(wsCtx(http.HandlerFunc(h.Read))))
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/initialize", roleSelf, h.Mutate)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/patch", roleSelf, h.Mutate)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/reorder", roleSelf, h.Mutate)
	r.authedMut("POST", "/api/v1/pages/{slug}/slides/restore", roleSelf, h.Mutate)
}
