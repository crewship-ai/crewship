package api

import (
	"context"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/craftapps"
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
	// Policy applies the instance administrator's Word, Sheet and agent
	// settings. Nil allows everything the profiles allow (package tests).
	Policy *craftapps.Policy
	// Components describes each application's runnable parts for Admin.
	Components map[string]craftapps.Component
	// Install installs an application's package (downloading it when
	// archive is nil) and returns its profile, ready to run. Nil means
	// applications cannot be installed while the server runs.
	Install CraftComponentInstall
	// Package-private trusted clock injection; server configuration uses the native boot clock.
	newOwnerClock func() (mooring.OwnerClock, error)
}

// CraftComponentInstall installs one application's package; see
// CraftNativeHTTPConfig.Install.
type CraftComponentInstall func(ctx context.Context, application string, archive io.Reader) (CraftNativeDocumentProfile, craftapps.Component, error)

func WithCraftNativeHTTP(c CraftNativeHTTPConfig) RouterOption {
	profiles := make(map[string]CraftNativeDocumentProfile, len(c.Profiles))
	for key, profile := range c.Profiles {
		profiles[key] = profile
	}
	c.Profiles = profiles
	components := make(map[string]craftapps.Component, len(c.Components))
	for key, component := range c.Components {
		components[key] = component
	}
	c.Components = components
	return func(r *Router) { r.craftNativeHTTPConfig = c }
}
func (r *Router) registerCraftNativeRoutes() {
	var clock mooring.OwnerClock
	var applications map[string]craftNativeApplication
	if r.craftNativeHTTPConfig.Enabled {
		var err error
		applications, r.craftNativeHTTPConfig.Profiles, err = r.qualifyStartupProfiles()
		if err != nil {
			r.craftNativeStartupErr = err
			return
		}
		factory := r.craftNativeHTTPConfig.newOwnerClock
		if factory == nil {
			factory = mooring.NewBootOwnerClock
		}
		clock, err = factory()
		if err != nil && (r.craftNativeHTTPConfig.Install == nil || !craftNativeOnlyReleaseProfiles(r.craftNativeHTTPConfig)) {
			r.craftNativeStartupErr = err
			return
		}
		if err != nil {
			// Release components on a system without a suspend-aware clock:
			// the server starts, Word and Sheet are unavailable and say why.
			r.logger.Warn("Word and Sheet are unavailable: no suspend-aware clock for edit locks", "error", err)
			for app := range r.craftNativeHTTPConfig.Components {
				r.craftNativeHTTPConfig.Components[app] = craftapps.Component{Reason: craftapps.ReasonClockUnsupported}
			}
			r.craftNativeHTTPConfig.Enabled, r.craftNativeHTTPConfig.AgentsEnabled, r.craftNativeHTTPConfig.Install = false, false, nil
			r.craftNativeHTTPConfig.Profiles, applications, clock = map[string]CraftNativeDocumentProfile{}, nil, nil
		}
	}
	m := newCraftNativeHTTP(r.pages, r.craftNativeHTTPConfig.Enabled, clock)
	if r.pages != nil {
		m.apps = newCraftAppsGate(r.craftNativeHTTPConfig.Policy, r.pages.db)
	}
	r.craftNativeHTTP = m
	d := newCraftNativeDocumentsHTTP(m, applications)
	for app, p := range r.craftNativeHTTPConfig.Profiles {
		d.profiles[app] = p
	}
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
	// openapi: responses 200,401,403,404,422,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/preview", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.preview)))))
	// openapi: responses 200,400,401,403,409,422,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/share", roleSelf, d.shareSnapshot)
	// openapi: responses 200,401,403,409,422,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/document", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.readSnapshot)))))
	// Read-only version history, same admission and private read authority as
	// the document read; the craft_version_history workspace flag keeps it off.
	// openapi: responses 200,401,403,404,409,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/document/versions", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.listVersions)))))
	// One historical version's bytes, read-only, same authority, audit and flag.
	// openapi: responses 200,400,401,403,404,409,422,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/document/versions/{version}", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.readVersion)))))
	// openapi: responses 200,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/save", roleSelf, d.saveSnapshot)
	// openapi: responses 200,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/import", roleSelf, d.importSnapshot)
	// openapi: responses 200,400,401,403,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/restore", roleSelf, d.restoreSnapshot)
	// openapi: responses 200,400,401,403,409,410,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/document/reconcile", roleSelf, d.reconcileSnapshot)
	// Agent proposals prepared while a human holds the lease (#4480).
	// openapi: responses 200,401,403,409,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/proposals", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.listProposals)))))
	// openapi: responses 200,401,403,404,409,503
	r.mux.Handle("GET /api/v1/pages/{slug}/craft/proposals/{proposalID}", authed(wsCtx(r.providerAccountPolicy(http.HandlerFunc(d.proposalDocument)))))
	// openapi: responses 200,400,401,403,404,409,413,422,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/proposals/{proposalID}/accept", roleSelf, d.acceptProposal)
	// openapi: responses 200,400,401,403,404,409,503
	r.authedMut("POST", "/api/v1/pages/{slug}/craft/proposals/{proposalID}/reject", roleSelf, d.rejectProposal)

}

// craftNativeOnlyReleaseProfiles reports whether every profile comes from a
// release package (none from an operator's explicit native configuration).
// Only then, and only while packages can be installed, does a missing clock
// leave the server running with Word and Sheet unavailable; an operator's
// explicit native configuration still refuses to start.
func craftNativeOnlyReleaseProfiles(c CraftNativeHTTPConfig) bool {
	for _, p := range c.Profiles {
		if !p.Bundled {
			return false
		}
	}
	return true
}

// qualifyStartupProfiles qualifies the operator's profiles together (any
// error refuses startup, as before) and each installed release package on
// its own: one that does not qualify leaves only that application
// unavailable, so installing a package can never take the server down.
func (r *Router) qualifyStartupProfiles() (map[string]craftNativeApplication, map[string]CraftNativeDocumentProfile, error) {
	operator := map[string]CraftNativeDocumentProfile{}
	release := map[string]CraftNativeDocumentProfile{}
	for app, p := range r.craftNativeHTTPConfig.Profiles {
		if p.Bundled {
			release[app] = p
		} else {
			operator[app] = p
		}
	}
	applications, profiles, err := qualifyCraftNativeProfiles(operator)
	if err != nil {
		return nil, nil, err
	}
	for app, p := range release {
		a, q, err := qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{app: p})
		if err != nil {
			r.logger.Error("installed Craft application cannot run", "application", app, "error", err)
			c := r.craftNativeHTTPConfig.Components[app]
			c.Available, c.Reason, c.Detail = false, craftapps.ReasonEngineStartFailed, err.Error()
			if r.craftNativeHTTPConfig.Components == nil {
				r.craftNativeHTTPConfig.Components = map[string]craftapps.Component{}
			}
			r.craftNativeHTTPConfig.Components[app] = c
			continue
		}
		applications[app], profiles[app] = a[app], q[app]
	}
	return applications, profiles, nil
}
