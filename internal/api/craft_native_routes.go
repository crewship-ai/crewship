package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// CraftNativeHTTPConfig is trusted startup policy for the separate native profile.
// Its zero value keeps the profile and every lease mutation unavailable.
type CraftNativeHTTPConfig struct{ Enabled bool }

func WithCraftNativeHTTP(c CraftNativeHTTPConfig) RouterOption {
	return func(r *Router) { r.craftNativeHTTPConfig = c }
}
func (r *Router) registerCraftNativeRoutes() {
	var clock mooring.OwnerClock
	if r.craftNativeHTTPConfig.Enabled {
		var err error
		clock, err = mooring.NewBootOwnerClock()
		if err != nil {
			r.craftNativeStartupErr = err
			return
		}
	}
	m := newCraftNativeHTTP(r.pages, r.craftNativeHTTPConfig.Enabled, clock)
	r.craftNativeHTTP = m
	authed, workspace := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// Availability is Page-scoped and never treats an old profile as native.
	// openapi: responses 200,401,403
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/availability", authed(workspace(r.providerAccountPolicy(http.HandlerFunc(m.availability)))))
	// Every mutation declares its per-object authority gate; the handler checks
	// original credentials and Page rights, including CLI read/write scopes.
	// openapi: responses 201,400,401,403,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/sessions", roleSelf, m.sessions)
	// openapi: responses 200,400,401,403,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/sessions/reauthorize", roleSelf, m.reauthorize)
	// openapi: responses 200,401,403
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/lease", authed(workspace(r.providerAccountPolicy(http.HandlerFunc(m.lease)))))
	// openapi: responses 200,401,403,409,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/events", authed(workspace(r.providerAccountPolicy(http.HandlerFunc(m.events)))))
	// openapi: responses 200,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/lease", roleSelf, m.acquire)
	// openapi: responses 200,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/lease/acquire", roleSelf, m.acquire)
	// openapi: responses 200,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/lease/renew", roleSelf, m.renew)
	// openapi: responses 200,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/lease/release", roleSelf, m.release)
	// openapi: responses 201,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/lease/request-handoff", roleSelf, m.requestHandoff)
	// openapi: responses 200,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/lease/handoff", roleSelf, m.handoff)
}
