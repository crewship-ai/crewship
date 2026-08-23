package api

// Route registration for the onboarding proposal store (§5.6, §8.2).
//
// Kept in its own file rather than folded into router_auth.go's
// registerAuthRoutes (which owns /api/v1/onboarding/status|complete|setup):
// those three are session-scoped with no workspace in context
// (authedSelfMut), while the proposal surface is workspace-scoped like
// crew-templates and needs RequireWorkspace. Splitting the registrar keeps
// each file's routes homogeneous in how they're wired.

import "net/http"

// registerOnboardingProposalRoutes wires the proposal store: Create and
// Apply are declared mutation routes (task #2/#3 — the normal authenticated,
// role-gated user path; MANAGER+ like CrewTemplateHandler.Deploy, since
// Apply performs exactly that mutation). Get is a plain workspace-scoped
// read.
func (r *Router) registerOnboardingProposalRoutes() {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	h := NewOnboardingProposalHandler(r.db, r.logger)
	h.SetJournal(r.Journal())

	r.authedMut("POST", "/api/v1/onboarding/proposals", roleCreate, h.Create)
	r.mux.Handle("GET /api/v1/onboarding/proposals/{id}", authed(wsCtx(http.HandlerFunc(h.Get))))
	r.authedMut("POST", "/api/v1/onboarding/proposals/{id}/apply", roleCreate, h.Apply)
}
