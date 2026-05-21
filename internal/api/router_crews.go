package api

// Routes for workspaces, crews, crew members, crew connections,
// integrations (MCP gateway), workflow templates, crew templates,
// AI crew wizard, agents (+ skills / credentials / chats / runs),
// credentials, skills, recipes, and crew provisioning.
//
// Returns the AgentHandler and ProvisioningHandler so the orchestrator
// can stash them on the Router for later setter wiring (scheduler,
// chatbridge auto-provision).

import "net/http"

// registerCrewsRoutes wires the entire "crews + agents + connections"
// surface. Constructs its own handlers; the only cross-domain
// references are the journal emitter and the WS hub (carried on the
// Router struct).
func (r *Router) registerCrewsRoutes() *ProvisioningHandler {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	ws := NewWorkspaceHandler(r.db, r.logger)
	crews := NewCrewHandler(r.db, r.logger)
	crewSocket := r.socketPath
	if crewSocket == "" {
		crewSocket = "/tmp/crewship.sock"
	}
	crews.SetSocketPath(crewSocket)
	if r.hub != nil {
		crews.SetHub(r.hub)
	}
	agents := NewAgentHandler(r.db, r.logger)
	r.agentHandler = agents
	if r.hub != nil {
		agents.SetHub(r.hub)
	}
	if r.scheduleUpdater != nil {
		agents.SetScheduler(r.scheduleUpdater)
	}
	agents.SetJournal(r.Journal())

	if r.license != nil {
		ws.SetLicense(r.license)
		crews.SetLicense(r.license)
		agents.SetLicense(r.license)
	}
	creds := NewCredentialHandler(r.db, r.logger)
	skills := NewSkillHandler(r.db, r.logger)
	skills.SetJournal(r.Journal())

	// Workspaces (auth only, no workspace context needed)
	r.mux.Handle("GET /api/v1/workspaces", authed(http.HandlerFunc(ws.List)))
	r.mux.Handle("POST /api/v1/workspaces", authed(http.HandlerFunc(ws.Create)))

	// Workspace detail (require workspace context via path param)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}", authed(wsCtx(http.HandlerFunc(ws.Get))))
	r.mux.Handle("PATCH /api/v1/workspaces/{workspaceId}", authed(wsCtx(http.HandlerFunc(ws.Update))))

	// Workspace members
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/members", authed(wsCtx(http.HandlerFunc(ws.ListMembers))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/members", authed(wsCtx(http.HandlerFunc(ws.AddMember))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/members/{memberId}", authed(wsCtx(http.HandlerFunc(ws.RemoveMember))))

	// Workspace invitations
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/invitations", authed(wsCtx(http.HandlerFunc(ws.ListInvitations))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/invitations", authed(wsCtx(http.HandlerFunc(ws.CreateInvitation))))

	// Crews (require workspace context)
	r.mux.Handle("GET /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.List))))
	r.mux.Handle("POST /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.Create))))
	r.mux.Handle("GET /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Get))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Update))))
	r.mux.Handle("PUT /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Delete))))

	// Per-crew autonomy policy (PR-B F2). Workspace-scoped list +
	// per-crew GET/PUT. PUT invalidates the shared resolver cache so
	// downstream subsystems (memory write gating, skill creation
	// HITL, behavior monitor, ephemeral spawn) see the new state
	// without waiting for the 10s TTL.
	policies := NewCrewPolicyHandler(r.db, r.PolicyResolver(), r.logger)
	policies.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/policies", authed(wsCtx(http.HandlerFunc(policies.List))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/policy", authed(wsCtx(http.HandlerFunc(policies.Get))))
	r.mux.Handle("PUT /api/v1/crews/{crewId}/policy", authed(wsCtx(http.HandlerFunc(policies.Put))))

	// Crew members
	r.mux.Handle("GET /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.ListMembers))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.AddMember))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/members/{memberId}", authed(wsCtx(http.HandlerFunc(crews.UpdateMemberRole))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/members/{memberId}", authed(wsCtx(http.HandlerFunc(crews.RemoveMember))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/apply-avatar-style", authed(wsCtx(http.HandlerFunc(crews.ApplyAvatarStyle))))

	// Crew Connections
	conns := NewCrewConnectionHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/crew-connections", authed(wsCtx(http.HandlerFunc(conns.List))))
	r.mux.Handle("POST /api/v1/crew-connections", authed(wsCtx(http.HandlerFunc(conns.Create))))
	r.mux.Handle("DELETE /api/v1/crew-connections/{connectionId}", authed(wsCtx(http.HandlerFunc(conns.Delete))))

	// Integrations (MCP Gateway)
	integrations := NewIntegrationHandler(r.db, r.logger)
	if r.hub != nil {
		integrations.SetHub(r.hub)
	}
	// Workspace-level integrations
	r.mux.Handle("GET /api/v1/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListWorkspaceIntegrations))))
	r.mux.Handle("POST /api/v1/integrations", authed(wsCtx(http.HandlerFunc(integrations.CreateWorkspaceIntegration))))
	r.mux.Handle("GET /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.GetWorkspaceIntegration))))
	r.mux.Handle("PATCH /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.UpdateWorkspaceIntegration))))
	r.mux.Handle("DELETE /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.DeleteWorkspaceIntegration))))
	r.mux.Handle("POST /api/v1/integrations/{integrationId}/test", authed(wsCtx(http.HandlerFunc(integrations.TestWorkspaceIntegrationConnection))))
	// All crew integrations (cross-crew overview for Integrations page)
	r.mux.Handle("GET /api/v1/integrations/crews", authed(wsCtx(http.HandlerFunc(integrations.ListAllCrewIntegrations))))
	// Crew-level integrations
	r.mux.Handle("GET /api/v1/crews/{crewId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListCrewIntegrations))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.CreateCrewIntegration))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.UpdateCrewIntegration))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.DeleteCrewIntegration))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/integrations/{integrationId}/test", authed(wsCtx(http.HandlerFunc(integrations.TestCrewIntegrationConnection))))
	// Recipes — 1-click curated bundles (CONNECTIONS.md §6)
	recipesH := NewRecipeHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/recipes", authed(http.HandlerFunc(recipesH.List)))
	r.mux.Handle("GET /api/v1/recipes/{slug}", authed(http.HandlerFunc(recipesH.Get)))
	r.mux.Handle("GET /api/v1/recipes/{slug}/preview", authed(wsCtx(http.HandlerFunc(recipesH.Preview))))
	r.mux.Handle("POST /api/v1/recipes/{slug}/install", authed(wsCtx(http.HandlerFunc(recipesH.Install))))
	// Credential audit timeline (CONNECTIONS.md §4.3 inline drawer)
	r.mux.Handle("GET /api/v1/credentials/{credentialId}/audit", authed(wsCtx(http.HandlerFunc(creds.AuditTimeline))))
	// Credential rotation w/ grace overlap (CONNECTIONS.md §7.1, MUST-add #1)
	r.mux.Handle("POST /api/v1/credentials/{credentialId}/rotate", authed(wsCtx(http.HandlerFunc(creds.Rotate))))
	r.mux.Handle("GET /api/v1/credentials/{credentialId}/rotations", authed(wsCtx(http.HandlerFunc(creds.ListRotations))))
	r.mux.Handle("DELETE /api/v1/credential-rotations/{rotationId}", authed(wsCtx(http.HandlerFunc(creds.CancelRotation))))
	// Per-tool granularity (Cursor parity, CONNECTIONS.md §3.1)
	r.mux.Handle("GET /api/v1/crews/{crewId}/integrations/{integrationId}/tools", authed(wsCtx(http.HandlerFunc(integrations.ListCrewIntegrationTools))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/integrations/{integrationId}/tools/{toolName}", authed(wsCtx(http.HandlerFunc(integrations.UpdateCrewIntegrationTool))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/integrations/{integrationId}/tools/refresh", authed(wsCtx(http.HandlerFunc(integrations.RefreshCrewIntegrationTools))))
	// Agent MCP bindings
	r.mux.Handle("GET /api/v1/agents/{agentId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListAgentBindings))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.CreateAgentBinding))))
	r.mux.Handle("PATCH /api/v1/agents/{agentId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.UpdateAgentBinding))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.DeleteAgentBinding))))
	// Resolve effective integrations for an agent (cascade: workspace → crew → agent bindings)
	r.mux.Handle("GET /api/v1/agents/{agentId}/integrations/resolved", authed(wsCtx(http.HandlerFunc(integrations.ResolveAgentIntegrations))))

	// Workflow Templates
	templates := NewTemplateHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/templates", authed(wsCtx(http.HandlerFunc(templates.List))))
	r.mux.Handle("POST /api/v1/templates", authed(wsCtx(http.HandlerFunc(templates.Create))))
	r.mux.Handle("GET /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Get))))
	r.mux.Handle("PATCH /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Update))))
	r.mux.Handle("DELETE /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Delete))))

	// Crew Templates (blueprints)
	crewTmpl := NewCrewTemplateHandler(r.db, r.logger)
	crewTmpl.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/crew-templates", authed(wsCtx(http.HandlerFunc(crewTmpl.List))))
	r.mux.Handle("GET /api/v1/crew-templates/{slug}", authed(wsCtx(http.HandlerFunc(crewTmpl.Get))))
	r.mux.Handle("POST /api/v1/crew-templates/{slug}/deploy", authed(wsCtx(http.HandlerFunc(crewTmpl.Deploy))))

	// AI crew wizard
	crewAI := NewCrewAIHandler(r.db, r.logger)
	crewAI.SetJournal(r.Journal())
	r.mux.Handle("POST /api/v1/crew-ai-suggest", authed(wsCtx(http.HandlerFunc(crewAI.Suggest))))

	// Agents (require workspace context)
	r.mux.Handle("GET /api/v1/agents/crews-status", authed(wsCtx(http.HandlerFunc(agents.CrewsStatus))))
	r.mux.Handle("GET /api/v1/agent-load", authed(wsCtx(http.HandlerFunc(agents.Load))))
	r.mux.Handle("GET /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.List))))
	r.mux.Handle("POST /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.Create))))
	r.mux.Handle("GET /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Get))))
	r.mux.Handle("PATCH /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Update))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Delete))))

	// Agent skills
	r.mux.Handle("GET /api/v1/agents/{agentId}/skills", authed(wsCtx(http.HandlerFunc(agents.ListSkills))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/skills", authed(wsCtx(http.HandlerFunc(agents.AddSkill))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/skills/{skillId}", authed(wsCtx(http.HandlerFunc(agents.RemoveSkill))))

	// Agent credentials
	r.mux.Handle("GET /api/v1/agents/{agentId}/credentials", authed(wsCtx(http.HandlerFunc(agents.ListCredentials))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/credentials", authed(wsCtx(http.HandlerFunc(agents.AddCredential))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/credentials/{assignmentId}", authed(wsCtx(http.HandlerFunc(agents.RemoveCredential))))

	// Agent chats & runs
	r.mux.Handle("GET /api/v1/agents/{agentId}/chats", authed(wsCtx(http.HandlerFunc(agents.ListChats))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/chats", authed(wsCtx(http.HandlerFunc(agents.CreateChat))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/runs", authed(wsCtx(http.HandlerFunc(agents.ListRuns))))

	// PR-E F6 — PERSONA endpoints (agent + crew flavors). Persona
	// handler shares the same policy resolver the routine + autonomy
	// surfaces use so the suggest endpoint stays consistent with
	// other agent-initiated actions.
	persona := NewPersonaHandler(r.db, r.logger, r.outputBasePath, r.PolicyResolver())
	r.mux.Handle("GET /api/v1/agents/{agentId}/persona", authed(wsCtx(http.HandlerFunc(persona.GetAgentPersona))))
	r.mux.Handle("PUT /api/v1/agents/{agentId}/persona", authed(wsCtx(http.HandlerFunc(persona.PutAgentPersona))))
	r.mux.Handle("DELETE /api/v1/agents/{agentId}/persona", authed(wsCtx(http.HandlerFunc(persona.DeleteAgentPersona))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/persona/history", authed(wsCtx(http.HandlerFunc(persona.GetAgentPersonaHistory))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/persona/suggest", authed(wsCtx(http.HandlerFunc(persona.SuggestAgentPersona))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/persona", authed(wsCtx(http.HandlerFunc(persona.GetCrewPersona))))
	r.mux.Handle("PUT /api/v1/crews/{crewId}/persona", authed(wsCtx(http.HandlerFunc(persona.PutCrewPersona))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/persona", authed(wsCtx(http.HandlerFunc(persona.DeleteCrewPersona))))

	// Credentials (require workspace context + manage role for create)
	r.mux.Handle("GET /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.List))))
	r.mux.Handle("POST /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.Create))))
	r.mux.Handle("POST /api/v1/credentials/test", authed(http.HandlerFunc(creds.Test)))
	r.mux.Handle("POST /api/v1/credentials/{credentialId}/test", authed(wsCtx(http.HandlerFunc(creds.TestStored))))
	r.mux.Handle("GET /api/v1/credentials/default-env-var", authed(http.HandlerFunc(creds.DefaultEnvVar)))
	r.mux.Handle("GET /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Get))))
	r.mux.Handle("PATCH /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Update))))
	r.mux.Handle("PUT /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Update))))
	r.mux.Handle("DELETE /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Delete))))

	// Skills (require auth)
	r.mux.Handle("GET /api/v1/skills", authed(wsCtx(http.HandlerFunc(skills.List))))
	r.mux.Handle("GET /api/v1/skills/{skillId}", authed(wsCtx(http.HandlerFunc(skills.Get))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/skills/import", authed(wsCtx(http.HandlerFunc(skills.Import))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/skills/{skillId}", authed(wsCtx(http.HandlerFunc(skills.Delete))))
	skillGen := NewSkillGenerateHandler(r.db, r.logger)
	// Path param name MUST match what the wsCtx middleware reads — the
	// pattern is {workspaceId} everywhere else in the API, and changing
	// it broke the workspace lookup on this route in the prior commit.
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/skills/generate", authed(wsCtx(http.HandlerFunc(skillGen.Generate))))
	skillBulk := NewSkillBulkImportHandler(r.db, r.logger)
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/skills/bulk-import", authed(wsCtx(http.HandlerFunc(skillBulk.Import))))

	// Devcontainer feature catalog (auth required, no workspace context needed).
	// Stash the handler on the router so cmd_start can wire it into chatbridge
	// for the auto-provision-on-first-message UX without a second instance.
	provisioning := NewProvisioningHandler(r.db, r.logger, r.catalogFetcher, r.runtimeFetcher, r.dockerClient, r.featureCacheDir, r.hub)
	r.provisioning = provisioning
	// Mirror provisioning lifecycle (queued / building / complete /
	// failed) into the unified Crew Journal alongside the existing WS
	// broadcast. Skipped when no journal is wired (early bring-up).
	if r.journal != nil {
		provisioning.SetJournal(r.journal)
	}
	r.mux.Handle("GET /api/v1/features/catalog", authed(http.HandlerFunc(provisioning.CatalogList)))
	r.mux.Handle("GET /api/v1/runtimes/catalog", authed(http.HandlerFunc(provisioning.RuntimeCatalogList)))

	// Crew provisioning (require workspace context)
	r.mux.Handle("GET /api/v1/crews/{crewId}/provision", authed(wsCtx(http.HandlerFunc(provisioning.ProvisionStatus))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/provision", authed(wsCtx(http.HandlerFunc(provisioning.ProvisionTrigger))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/rebuild", authed(wsCtx(http.HandlerFunc(provisioning.ProvisionRebuild))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/restart-agents", authed(wsCtx(http.HandlerFunc(provisioning.RestartCrewAgents))))

	// Devcontainer image cache management (GC)
	r.mux.Handle("GET /api/v1/cache/images", authed(wsCtx(http.HandlerFunc(provisioning.CacheList))))
	r.mux.Handle("DELETE /api/v1/cache/images/{tag}", authed(wsCtx(http.HandlerFunc(provisioning.CacheDelete))))

	return provisioning
}
