package api

// registerRoutes is the top-level orchestrator for HTTP route
// registration. It calls per-domain registrars in a fixed order;
// each registrar owns construction of its handlers and the
// `r.mux.Handle(...)` lines for its surface.
//
// Cross-domain handlers (pipelines / assignments / queries /
// port-expose) are constructed in the public-side registrar and
// passed to registerInternalRoutes so the public + internal
// surfaces share state.

func (r *Router) registerRoutes() {
	// Health, system runtime, license, keeper status.
	r.registerSystemRoutes()

	// Workspaces, crews, members, integrations, templates, agents,
	// credentials, skills, provisioning, recipes.
	r.registerCrewsRoutes()

	// Public pipeline routes; returns the handler so the internal
	// /pipelines/save endpoint can re-use it.
	pipes := r.registerPipelineRoutes()

	// Missions, issues, projects, journal, paymaster, approvals,
	// inbox, eval, queries (public), assignments (public),
	// proxy/files, port-expose (public + capability URL), OAuth,
	// MCP audit/registry, webhook trigger. Returns shared handlers
	// (assign, queries, portExposeH) for the internal registrar.
	oh := r.registerOrchestrationRoutes()

	// Wire the dispatch-time provisioning gate: the AssignmentHandler (created
	// in registerOrchestrationRoutes) uses the ProvisioningHandler (created in
	// registerCrewsRoutes, above) to ensure a crew's devcontainer image is
	// built before an issue/mission run is dispatched — otherwise a cold crew
	// launches from the bare runtime image and the agent exits 127.
	if r.assignmentHandler != nil && r.provisioning != nil {
		r.assignmentHandler.SetProvisioner(r.provisioning)
	}

	// Auth, signup, Google OAuth2, sessions, CLI tokens, NextAuth,
	// onboarding.
	r.registerAuthRoutes()

	// Admin stats, audit, keeper admin log, backups.
	r.registerAdminRoutes()

	// Slash-command catalog (GET /api/v1/slash-commands) — feeds
	// the chat composer palette and the CLI repl autocomplete with
	// capability-filtered actions. Admin capability-management
	// routes land alongside in PRD-SLASH-CAPABILITIES-2026 commit 8.
	r.registerSlashRoutes()

	// Sidecar IPC — every /api/v1/internal/* endpoint plus the
	// keeper internal surface and cross-crew messaging.
	r.registerInternalRoutes(pipes, oh)
}
