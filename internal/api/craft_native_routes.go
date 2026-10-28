package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// CraftNativeHTTPConfig is trusted startup policy for the separate native profile.
// Its zero value keeps the profile and every lease mutation unavailable.
type CraftNativeHTTPConfig struct {
	Enabled       bool
	Profiles      map[string]CraftNativeDocumentProfile
	AgentsEnabled bool
	AgentApply    CraftNativeAgentApply
	AgentInspect  CraftNativeAgentInspect
}

func WithCraftNativeHTTP(c CraftNativeHTTPConfig) RouterOption {
	profiles := make(map[string]CraftNativeDocumentProfile, len(c.Profiles))
	for key, profile := range c.Profiles {
		profiles[key] = profile
	}
	c.Profiles = profiles
	return func(r *Router) { r.craftNativeHTTPConfig = c }
}
func (r *Router) registerCraftNativeRoutes() {
	var clock mooring.OwnerClock
	var applications map[string]craftNativeApplication
	if r.craftNativeHTTPConfig.Enabled {
		var err error
		applications, err = qualifyCraftNativeProfiles(r.craftNativeHTTPConfig.Profiles)
		if err != nil {
			r.craftNativeStartupErr = err
			return
		}
		clock, err = mooring.NewBootOwnerClock()
		if err != nil {
			r.craftNativeStartupErr = err
			return
		}
	}
	m := newCraftNativeHTTP(r.pages, r.craftNativeHTTPConfig.Enabled, clock)
	r.craftNativeHTTP = m
	d := newCraftNativeDocumentsHTTP(m, applications)
	r.craftNativeDocuments = d
	m.agents = newCraftNativeAgentManager(m, d, r.craftNativeHTTPConfig)
	_ = m.agents.start(r.webhookHandler)
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// Availability is Page-scoped and never treats an old profile as native.
	// openapi: responses 200,401,403
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/availability", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(m.availability)))))
	// Every mutation declares its per-object authority gate; the handler checks
	// original credentials and Page rights, including CLI read/write scopes.
	// openapi: responses 201,400,401,403,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/sessions", roleSelf, m.sessions)
	// openapi: responses 202,400,401,403,409,413,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/tasks", roleSelf, m.agents.task)
	// openapi: responses 200,401,403,404,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/tasks/{workID}", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(m.agents.taskStatus)))))
	// openapi: responses 200,202,400,401,403,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/tasks/cancel", roleSelf, m.agents.cancelTask)
	// openapi: responses 200,400,401,403,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/sessions/reauthorize", roleSelf, m.reauthorize)
	// openapi: responses 200,401,403
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/lease", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(m.lease)))))
	// openapi: responses 200,401,403,409,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/events", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(m.events)))))
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
	// openapi: responses 201,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/initialize", roleSelf, d.initialize)
	// openapi: responses 200,401,403,409,422,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/document", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.readSnapshot)))))
	// openapi: responses 200,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/save", roleSelf, d.saveSnapshot)
	// openapi: responses 200,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/import", roleSelf, d.importSnapshot)
	// openapi: responses 200,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/restore", roleSelf, d.restoreSnapshot)
	// openapi: responses 200,400,401,403,409,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/reconcile", roleSelf, d.reconcileSnapshot)

}
