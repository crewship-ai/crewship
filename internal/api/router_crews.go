package api

// Routes for workspaces, crews, crew members, crew connections,
// integrations (MCP gateway), workflow templates, crew templates,
// AI crew wizard, agents (+ skills / credentials / chats / runs),
// credentials, skills, recipes, and crew provisioning.
//
// Returns the AgentHandler and ProvisioningHandler so the orchestrator
// can stash them on the Router for later setter wiring (scheduler,
// chatbridge auto-provision).

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/config"
)

// registerCrewsRoutes wires the entire "crews + agents + connections"
// surface. Constructs its own handlers; the only cross-domain
// references are the journal emitter and the WS hub (carried on the
// Router struct).
func (r *Router) registerCrewsRoutes() *ProvisioningHandler {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	ws := NewWorkspaceHandler(r.db, r.logger)
	if r.hub != nil {
		ws.SetHub(r.hub)
	}
	crews := NewCrewHandler(r.db, r.logger)
	crewSocket := r.socketPath
	if crewSocket == "" {
		crewSocket = config.DefaultSocketPath()
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
	// PR-D F5: wire the shared per-crew autonomy resolver so the Hire
	// / Rehire handlers gate ephemeral spawn per crew policy. Same
	// instance the policies handler uses below — flipping policy via
	// PUT invalidates it for everyone, including the hire path.
	agents.SetPolicyResolver(r.PolicyResolver())

	if r.license != nil {
		ws.SetLicense(r.license)
		crews.SetLicense(r.license)
		agents.SetLicense(r.license)
	}
	creds := NewCredentialHandler(r.db, r.logger)
	// Revoke → remove file-based /secrets from running containers (#814).
	// Reuses the container provider already wired for the keeper's exec path;
	// nil (tests / --no-docker) makes reconciliation a no-op.
	creds.SetContainer(r.keeperContainer)
	// Stash on the router so registerInternalRoutes can wire the
	// /api/v1/internal/credentials Create + Rotate adapter against
	// the same instance the public surface uses.
	r.credentialHandler = creds
	skills := NewSkillHandler(r.db, r.logger)
	skills.SetJournal(r.Journal())

	// Workspaces (auth only, no workspace context needed)
	r.mux.Handle("GET /api/v1/workspaces", authed(http.HandlerFunc(ws.List)))
	r.authedSelfMut("POST", "/api/v1/workspaces", ws.Create)

	// Workspace detail (require workspace context via path param)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}", authed(wsCtx(http.HandlerFunc(ws.Get))))
	r.authedMut("PATCH", "/api/v1/workspaces/{workspaceId}", roleManage, ws.Update)
	// Delete is OWNER-only + slug-confirm + last-workspace guard, all
	// enforced inside the handler; the route gate is roleManage (ADMIN+)
	// so a MANAGER never even reaches the finer OWNER check (#866.2).
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}", roleManage, ws.Delete)

	// Workspace members
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/members", authed(wsCtx(http.HandlerFunc(ws.ListMembers))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/members", roleManage, ws.AddMember)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/members/{memberId}", roleManage, ws.RemoveMember)
	// Member role change (#867.2). MANAGER+ at the route (roleCreate); the
	// ladder (grant-below-own, no-modify-superior, last-owner) is enforced
	// in the handler. Literal path — the "/capabilities" suffix routes
	// below win their more-specific match, so no ordering hazard.
	r.authedMut("PATCH", "/api/v1/workspaces/{workspaceId}/members/{memberId}", roleCreate, ws.UpdateMemberRole)
	// PRD-SLASH-CAPABILITIES-2026 §6.7 — per-member capability
	// grant/revoke surface. The PATCH is ADMIN+ enforced at registration
	// (authedMut/roleManage); the GET is read-only, JWT-authed +
	// workspace-scoped.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities",
		authed(wsCtx(http.HandlerFunc(ws.GetMemberCapabilities))))
	r.authedMut("PATCH", "/api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities", roleManage, ws.PatchMemberCapabilities)
	// Bulk variant — drives the Members capability grid in one
	// round-trip instead of N+1 fan-out across per-member endpoints.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/members/capabilities",
		authed(wsCtx(http.HandlerFunc(ws.ListMembersCapabilities))))

	// Workspace invitations
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/invitations", authed(wsCtx(http.HandlerFunc(ws.ListInvitations))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/invitations", roleManage, ws.CreateInvitation)

	// Crews (require workspace context)
	r.mux.Handle("GET /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.List))))
	r.authedMut("POST", "/api/v1/crews", roleCreate, crews.Create)
	r.mux.Handle("GET /api/v1/crews/{crewId}", authed(wsCtx(http.HandlerFunc(crews.Get))))
	// One-shot authoring dump: DSL schema + container caps + integration tools
	// + agents + runtimes (#862). Literal "capabilities" beats the {crewId}
	// catch-all in net/http matching, so no ordering hazard.
	r.mux.Handle("GET /api/v1/crews/{crewId}/capabilities", authed(wsCtx(http.HandlerFunc(crews.Capabilities))))
	r.authedMut("PATCH", "/api/v1/crews/{crewId}", roleManage, crews.Update)
	r.authedMut("PUT", "/api/v1/crews/{crewId}", roleManage, crews.Update)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}", roleManage, crews.Delete)

	// Per-crew autonomy policy (PR-B F2). Workspace-scoped list +
	// per-crew GET/PUT. PUT invalidates the shared resolver cache so
	// downstream subsystems (memory write gating, skill creation
	// HITL, behavior monitor, ephemeral spawn) see the new state
	// without waiting for the 10s TTL.
	policies := NewCrewPolicyHandler(r.db, r.PolicyResolver(), r.logger)
	policies.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/policies", authed(wsCtx(http.HandlerFunc(policies.List))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/policy", authed(wsCtx(http.HandlerFunc(policies.Get))))
	r.authedMut("PUT", "/api/v1/crews/{crewId}/policy", roleCreate, policies.Put)

	// Crew members
	r.mux.Handle("GET /api/v1/crews/{crewId}/members", authed(wsCtx(http.HandlerFunc(crews.ListMembers))))
	r.authedMut("POST", "/api/v1/crews/{crewId}/members", roleCreate, crews.AddMember)
	r.authedMut("PATCH", "/api/v1/crews/{crewId}/members/{memberId}", roleManage, crews.UpdateMemberRole)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/members/{memberId}", roleManage, crews.RemoveMember)
	r.authedMut("POST", "/api/v1/crews/{crewId}/apply-avatar-style", roleManage, crews.ApplyAvatarStyle)
	r.mux.Handle("GET /api/v1/crews/{crewId}/container-status", authed(wsCtx(http.HandlerFunc(crews.ContainerStatus))))

	// Crew Connections
	conns := NewCrewConnectionHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/crew-connections", authed(wsCtx(http.HandlerFunc(conns.List))))
	r.authedMut("POST", "/api/v1/crew-connections", roleCreate, conns.Create)
	r.authedMut("DELETE", "/api/v1/crew-connections/{connectionId}", roleCreate, conns.Delete)

	// Integrations (MCP Gateway)
	integrations := NewIntegrationHandler(r.db, r.logger)
	if r.hub != nil {
		integrations.SetHub(r.hub)
	}
	// Workspace-level integrations
	r.mux.Handle("GET /api/v1/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListWorkspaceIntegrations))))
	r.authedMut("POST", "/api/v1/integrations", roleManage, integrations.CreateWorkspaceIntegration)
	r.mux.Handle("GET /api/v1/integrations/{integrationId}", authed(wsCtx(http.HandlerFunc(integrations.GetWorkspaceIntegration))))
	r.authedMut("PATCH", "/api/v1/integrations/{integrationId}", roleManage, integrations.UpdateWorkspaceIntegration)
	r.authedMut("DELETE", "/api/v1/integrations/{integrationId}", roleManage, integrations.DeleteWorkspaceIntegration)
	r.authedMut("POST", "/api/v1/integrations/{integrationId}/test", roleCreate, integrations.TestWorkspaceIntegrationConnection)
	// All crew integrations (cross-crew overview for Integrations page)
	r.mux.Handle("GET /api/v1/integrations/crews", authed(wsCtx(http.HandlerFunc(integrations.ListAllCrewIntegrations))))
	// Crew-level integrations
	r.mux.Handle("GET /api/v1/crews/{crewId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListCrewIntegrations))))
	r.authedMut("POST", "/api/v1/crews/{crewId}/integrations", roleCreate, integrations.CreateCrewIntegration)
	r.authedMut("PATCH", "/api/v1/crews/{crewId}/integrations/{integrationId}", roleManage, integrations.UpdateCrewIntegration)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/integrations/{integrationId}", roleManage, integrations.DeleteCrewIntegration)
	r.authedMut("POST", "/api/v1/crews/{crewId}/integrations/{integrationId}/test", roleCreate, integrations.TestCrewIntegrationConnection)
	// Composio managed integrations — read-only inventory (auth-config catalog
	// + connected accounts grouped by user_id). Two literal path segments after
	// /integrations so it never collides with the /{integrationId} wildcard.
	composioH := NewComposioHandler(r.db, r.logger, r.composioConfig)
	r.mux.Handle("GET /api/v1/integrations/composio/inventory", authed(wsCtx(http.HandlerFunc(composioH.ListInventory))))
	r.mux.Handle("GET /api/v1/integrations/composio/toolkits", authed(wsCtx(http.HandlerFunc(composioH.ListToolkits))))
	r.mux.Handle("GET /api/v1/integrations/composio/tools", authed(wsCtx(http.HandlerFunc(composioH.ListTools))))
	r.mux.Handle("GET /api/v1/integrations/composio/triggers", authed(wsCtx(http.HandlerFunc(composioH.ListTriggerTypes))))
	r.mux.Handle("GET /api/v1/integrations/composio/triggers/active", authed(wsCtx(http.HandlerFunc(composioH.ListActiveTriggers))))
	r.authedMut("POST", "/api/v1/integrations/composio/triggers", roleManage, composioH.CreateTrigger)
	r.mux.Handle("GET /api/v1/integrations/composio/settings", authed(wsCtx(http.HandlerFunc(composioH.GetSettings))))
	r.authedMut("PUT", "/api/v1/integrations/composio/settings", roleManage, composioH.UpsertSettings)
	r.authedMut("DELETE", "/api/v1/integrations/composio/settings", roleManage, composioH.DeleteSettings)
	r.authedMut("POST", "/api/v1/integrations/composio/connect", roleManage, composioH.Connect)
	// Default connector — inspect (read) / provision (manage) the workspace-wide
	// default Composio MCP server every agent inherits when
	// COMPOSIO_DEFAULT_CONNECTOR is ON and the agent has no per-agent binding.
	r.mux.Handle("GET /api/v1/integrations/composio/default", authed(wsCtx(http.HandlerFunc(composioH.GetDefault))))
	r.authedMut("PUT", "/api/v1/integrations/composio/default", roleManage, composioH.SetDefault)
	// Agent access binding — assign a Composio user (its connected accounts/
	// tools) to a specific agent, persisting the credential + workspace MCP
	// server + agent binding the runtime resolver already reads.
	r.mux.Handle("GET /api/v1/integrations/composio/agents/{agentId}/bind", authed(wsCtx(http.HandlerFunc(composioH.ListAgentBindings))))
	r.authedMut("POST", "/api/v1/integrations/composio/agents/{agentId}/bind", roleManage, composioH.BindAgent)
	r.authedMut("DELETE", "/api/v1/integrations/composio/agents/{agentId}/bind", roleManage, composioH.UnbindAgent)
	// Connected-account management — revoke/refresh/delete a Composio connected
	// account (manage-gated; proxies the matching Composio lifecycle call).
	r.authedMut("POST", "/api/v1/integrations/composio/accounts/{accountId}/revoke", roleManage, composioH.RevokeAccount)
	r.authedMut("POST", "/api/v1/integrations/composio/accounts/{accountId}/refresh", roleManage, composioH.RefreshAccount)
	r.authedMut("DELETE", "/api/v1/integrations/composio/accounts/{accountId}", roleManage, composioH.DeleteAccount)
	// Connectors — curated manifest catalog + install flow. List/Get are
	// catalog browse (auth only, no workspace context); Verify/Install
	// mutate workspace state and gate on MANAGER+ via wsCtx-resolved role.
	connectorsH := NewConnectorHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/connectors", authed(http.HandlerFunc(connectorsH.List)))
	r.mux.Handle("GET /api/v1/connectors/{connectorId}", authed(http.HandlerFunc(connectorsH.Get)))
	r.authedMut("POST", "/api/v1/connectors/{connectorId}/verify", roleCreate, connectorsH.Verify)
	r.authedMut("POST", "/api/v1/connectors/{connectorId}/install", roleCreate, connectorsH.Install)
	// Recipes — 1-click curated bundles (CONNECTIONS.md §6)
	recipesH := NewRecipeHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/recipes", authed(http.HandlerFunc(recipesH.List)))
	r.mux.Handle("GET /api/v1/recipes/{slug}", authed(http.HandlerFunc(recipesH.Get)))
	r.mux.Handle("GET /api/v1/recipes/{slug}/preview", authed(wsCtx(http.HandlerFunc(recipesH.Preview))))
	r.authedMut("POST", "/api/v1/recipes/{slug}/install", roleManage, recipesH.Install)
	// Credential audit timeline (CONNECTIONS.md §4.3 inline drawer)
	r.mux.Handle("GET /api/v1/credentials/{credentialId}/audit", authed(wsCtx(http.HandlerFunc(creds.AuditTimeline))))
	// Credential rotation w/ grace overlap (CONNECTIONS.md §7.1, MUST-add #1).
	// roleInline: the handlers run the layered ADMIN+-or-credential.rotate
	// gate (requireRoleOrCapabilityOrForbid) — a roleManage middleware gate
	// here would 403 capability-granted MANAGERs/MEMBERs before the handler
	// could honour the grant.
	r.authedMut("POST", "/api/v1/credentials/{credentialId}/rotate", roleInline, creds.Rotate)
	r.mux.Handle("GET /api/v1/credentials/{credentialId}/rotations", authed(wsCtx(http.HandlerFunc(creds.ListRotations))))
	r.authedMut("DELETE", "/api/v1/credential-rotations/{rotationId}", roleInline, creds.CancelRotation)
	// Per-tool granularity (Cursor parity, CONNECTIONS.md §3.1)
	r.mux.Handle("GET /api/v1/crews/{crewId}/integrations/{integrationId}/tools", authed(wsCtx(http.HandlerFunc(integrations.ListCrewIntegrationTools))))
	r.authedMut("PATCH", "/api/v1/crews/{crewId}/integrations/{integrationId}/tools/{toolName}", roleManage, integrations.UpdateCrewIntegrationTool)
	r.authedMut("POST", "/api/v1/crews/{crewId}/integrations/{integrationId}/tools/refresh", roleManage, integrations.RefreshCrewIntegrationTools)
	// Agent MCP bindings
	r.mux.Handle("GET /api/v1/agents/{agentId}/integrations", authed(wsCtx(http.HandlerFunc(integrations.ListAgentBindings))))
	r.authedMut("POST", "/api/v1/agents/{agentId}/integrations", roleCreate, integrations.CreateAgentBinding)
	r.authedMut("PATCH", "/api/v1/agents/{agentId}/integrations/{integrationId}", roleCreate, integrations.UpdateAgentBinding)
	r.authedMut("DELETE", "/api/v1/agents/{agentId}/integrations/{integrationId}", roleCreate, integrations.DeleteAgentBinding)
	// Resolve effective integrations for an agent (cascade: workspace → crew → agent bindings)
	r.mux.Handle("GET /api/v1/agents/{agentId}/integrations/resolved", authed(wsCtx(http.HandlerFunc(integrations.ResolveAgentIntegrations))))

	// Workflow Templates
	templates := NewTemplateHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/templates", authed(wsCtx(http.HandlerFunc(templates.List))))
	r.authedMut("POST", "/api/v1/templates", roleCreate, templates.Create)
	r.mux.Handle("GET /api/v1/templates/{templateId}", authed(wsCtx(http.HandlerFunc(templates.Get))))
	r.authedMut("PATCH", "/api/v1/templates/{templateId}", roleCreate, templates.Update)
	r.authedMut("DELETE", "/api/v1/templates/{templateId}", roleCreate, templates.Delete)

	// Crew Templates (blueprints)
	crewTmpl := NewCrewTemplateHandler(r.db, r.logger)
	crewTmpl.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/crew-templates", authed(wsCtx(http.HandlerFunc(crewTmpl.List))))
	r.mux.Handle("GET /api/v1/crew-templates/{slug}", authed(wsCtx(http.HandlerFunc(crewTmpl.Get))))
	r.authedMut("POST", "/api/v1/crew-templates/{slug}/deploy", roleCreate, crewTmpl.Deploy)

	// AI crew wizard
	crewAI := NewCrewAIHandler(r.db, r.logger)
	crewAI.SetJournal(r.Journal())
	r.authedMut("POST", "/api/v1/crew-ai-suggest", roleCreate, crewAI.Suggest)

	// Model discovery — live-or-curated per provider. The agent update path
	// reuses this same resolver to validate llm_model, so the handler is also
	// stashed on the AgentHandler as its ModelValidator.
	ollamaURL := ""
	if r.keeperConfig != nil {
		ollamaURL = r.keeperConfig.OllamaURL
	}
	models := NewModelsHandler(r.db, r.logger, ollamaURL)
	r.mux.Handle("GET /api/v1/models", authed(wsCtx(http.HandlerFunc(models.List))))
	agents.SetModelValidator(models)

	// Agents (require workspace context)
	r.mux.Handle("GET /api/v1/agents/crews-status", authed(wsCtx(http.HandlerFunc(agents.CrewsStatus))))
	r.mux.Handle("GET /api/v1/agent-load", authed(wsCtx(http.HandlerFunc(agents.Load))))
	r.mux.Handle("GET /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.List))))
	r.authedMut("POST", "/api/v1/agents", roleInline, agents.Create)
	// PR-D F5 ephemeral lifecycle endpoints. Hire creates a new
	// short-lived agent gated by the per-crew autonomy policy.
	// Rehire resets the TTL on an existing ephemeral (typically a
	// ghost) so the operator can extend a hire without losing the
	// agent's memory continuity.
	r.authedMut("POST", "/api/v1/agents/hire", roleCreate, agents.Hire)
	r.authedMut("POST", "/api/v1/agents/{agentId}/rehire", roleCreate, agents.Rehire)
	// Approve-hire flips a guided-autonomy ephemeral from
	// PENDING_REVIEW to IDLE, releasing the chatbridge guard so the
	// agent can serve messages. Paired with the blocking inbox
	// waitpoint written by Hire when policy returns InboxApprove.
	r.authedMut("POST", "/api/v1/agents/{agentId}/approve-hire", roleCreate, agents.ApproveHire)
	r.mux.Handle("GET /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Get))))
	r.authedMut("PATCH", "/api/v1/agents/{agentId}", roleInline, agents.Update)
	r.authedMut("DELETE", "/api/v1/agents/{agentId}", roleInline, agents.Delete)
	// Webhook signing secret is show-once (#999): no read endpoint exists,
	// rotate mints + returns the new value exactly once. Gate is inline
	// (canEditAgent) — same as Update.
	r.authedMut("POST", "/api/v1/agents/{agentId}/webhook-secret/rotate", roleInline, agents.RotateWebhookSecret)

	// Agent skills
	r.mux.Handle("GET /api/v1/agents/{agentId}/skills", authed(wsCtx(http.HandlerFunc(agents.ListSkills))))
	r.authedMut("POST", "/api/v1/agents/{agentId}/skills", roleCreate, agents.AddSkill)
	r.authedMut("DELETE", "/api/v1/agents/{agentId}/skills/{skillId}", roleCreate, agents.RemoveSkill)

	// Agent credentials
	r.mux.Handle("GET /api/v1/agents/{agentId}/credentials", authed(wsCtx(http.HandlerFunc(agents.ListCredentials))))
	r.authedMut("POST", "/api/v1/agents/{agentId}/credentials", roleManage, agents.AddCredential)
	r.authedMut("DELETE", "/api/v1/agents/{agentId}/credentials/{assignmentId}", roleManage, agents.RemoveCredential)

	// Agent chats & runs
	r.mux.Handle("GET /api/v1/agents/{agentId}/chats", authed(wsCtx(http.HandlerFunc(agents.ListChats))))
	r.authedMut("POST", "/api/v1/agents/{agentId}/chats", roleSelf, agents.CreateChat)
	// Mark-read: advances the caller's per-chat read cursor (unread badge
	// source) and clears the paired "agent replied" inbox item.
	r.authedMut("PUT", "/api/v1/agents/{agentId}/chats/{chatId}/read", roleSelf, agents.MarkChatRead)
	// Delete: creator-or-agent-editor gate lives in the handler (#998 —
	// lets one-shot programmatic chats clean up after themselves).
	r.authedMut("DELETE", "/api/v1/agents/{agentId}/chats/{chatId}", roleInline, agents.DeleteChat)
	r.mux.Handle("GET /api/v1/agents/{agentId}/runs", authed(wsCtx(http.HandlerFunc(agents.ListRuns))))

	// PR-E F6 — PERSONA endpoints (agent + crew flavors). Persona
	// handler shares the same policy resolver the routine + autonomy
	// surfaces use so the suggest endpoint stays consistent with
	// other agent-initiated actions.
	persona := NewPersonaHandler(r.db, r.logger, r.outputBasePath, r.PolicyResolver())
	r.mux.Handle("GET /api/v1/agents/{agentId}/persona", authed(wsCtx(http.HandlerFunc(persona.GetAgentPersona))))
	r.authedMut("PUT", "/api/v1/agents/{agentId}/persona", roleCreate, persona.PutAgentPersona)
	r.authedMut("DELETE", "/api/v1/agents/{agentId}/persona", roleCreate, persona.DeleteAgentPersona)
	r.mux.Handle("GET /api/v1/agents/{agentId}/persona/history", authed(wsCtx(http.HandlerFunc(persona.GetAgentPersonaHistory))))
	r.authedMut("POST", "/api/v1/agents/{agentId}/persona/suggest", roleCreate, persona.SuggestAgentPersona)
	r.mux.Handle("GET /api/v1/crews/{crewId}/persona", authed(wsCtx(http.HandlerFunc(persona.GetCrewPersona))))
	r.authedMut("PUT", "/api/v1/crews/{crewId}/persona", roleCreate, persona.PutCrewPersona)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/persona", roleCreate, persona.DeleteCrewPersona)

	// PR-G F4.1 UX — per-agent self-learning posture (v106). Flag
	// governs whether keeper evaluator ALLOW decisions auto-apply
	// (lessons land, skills activate) or queue an inbox item. PATCH
	// requires ADMIN+ because flipping this weakens the inbox
	// approval contract that protects production agents.
	learning := NewLearningHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/agents/{agentId}/learning", authed(wsCtx(http.HandlerFunc(learning.Get))))
	r.authedMut("PATCH", "/api/v1/agents/{agentId}/learning", roleManage, learning.Patch)

	// PR-E F6 — Peer card endpoints (per-agent operator view).
	peers := NewPeerCardHandler(r.db, r.logger, r.outputBasePath)
	r.mux.Handle("GET /api/v1/agents/{agentId}/peers", authed(wsCtx(http.HandlerFunc(peers.ListAgentPeers))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/peers/{userId}", authed(wsCtx(http.HandlerFunc(peers.GetAgentPeer))))
	r.authedMut("DELETE", "/api/v1/agents/{agentId}/peers/{userId}", roleSelf, peers.DeleteAgentPeer)

	// PR-E F6 — GDPR primitives. User-facing /users/me/* — every
	// authenticated user can act on their own peer cards without
	// needing workspace-admin. Cross-user / admin-driven deletion
	// lives behind /admin/users/{id}/data in Phase 2.
	privacy := NewUserPeerPrivacyHandler(r.db, r.logger, r.outputBasePath)
	r.mux.Handle("GET /api/v1/users/me/peer-consent", authed(wsCtx(http.HandlerFunc(privacy.GetConsent))))
	r.authedMut("PUT", "/api/v1/users/me/peer-consent", roleSelf, privacy.PutConsent)
	r.mux.Handle("GET /api/v1/users/me/peer-cards", authed(wsCtx(http.HandlerFunc(privacy.GetMyCards))))
	r.authedMut("DELETE", "/api/v1/users/me/peer-cards", roleSelf, privacy.DeleteMyCards)

	// Credentials (require workspace context + manage role for create)
	r.mux.Handle("GET /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.List))))
	r.authedMut("POST", "/api/v1/credentials", roleInline, creds.Create)
	r.authedSelfMut("POST", "/api/v1/credentials/test", creds.Test)
	r.authedMut("POST", "/api/v1/credentials/{credentialId}/test", roleCreate, creds.TestStored)
	// #1083: wrap in wsCtx like every other credentials route. The response
	// carries no tenant data, but requiring workspace membership keeps this
	// route uniform with the rest of the credentials surface.
	r.mux.Handle("GET /api/v1/credentials/default-env-var", authed(wsCtx(http.HandlerFunc(creds.DefaultEnvVar))))
	r.mux.Handle("GET /api/v1/credentials/{credentialId}", authed(wsCtx(http.HandlerFunc(creds.Get))))
	r.authedMut("PATCH", "/api/v1/credentials/{credentialId}", roleCreate, creds.Update)
	r.authedMut("PUT", "/api/v1/credentials/{credentialId}", roleCreate, creds.Update)
	r.authedMut("DELETE", "/api/v1/credentials/{credentialId}", roleManage, creds.Delete)

	// Skills (require auth)
	r.mux.Handle("GET /api/v1/skills", authed(wsCtx(http.HandlerFunc(skills.List))))
	r.mux.Handle("GET /api/v1/skills/{skillId}", authed(wsCtx(http.HandlerFunc(skills.Get))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/skills/import", roleCreate, skills.Import)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/skills/{skillId}", roleManage, skills.Delete)
	skillGen := NewSkillGenerateHandler(r.db, r.logger)
	// Same stash-for-reuse pattern as creds above — the internal
	// /api/v1/internal/skills/generate adapter shares the instance
	// (per-workspace LLM credential cache must not fork).
	r.skillGenHandler = skillGen
	// Path param name MUST match what the wsCtx middleware reads — the
	// pattern is {workspaceId} everywhere else in the API, and changing
	// it broke the workspace lookup on this route in the prior commit.
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/skills/generate", roleInline, skillGen.Generate)
	skillBulk := NewSkillBulkImportHandler(r.db, r.logger)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/skills/bulk-import", roleCreate, skillBulk.Import)

	// Devcontainer feature catalog (auth required, no workspace context needed).
	// Stash the handler on the router so cmd_start can wire it into chatbridge
	// for the auto-provision-on-first-message UX without a second instance.
	provisioning := NewProvisioningHandler(r.db, r.logger, r.catalogFetcher, r.runtimeFetcher, r.dockerClient, r.featureCacheDir, r.hub)
	r.provisioning = provisioning
	// Proactive provisioning: building the devcontainer image starts the moment
	// a crew is created or its config changes (CrewHandler.maybeAutoProvision),
	// so it's ready before the first dispatch — no manual "Build now" step.
	crews.SetProvisioner(provisioning)
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
	r.authedMut("POST", "/api/v1/crews/{crewId}/provision", roleCreate, provisioning.ProvisionTrigger)
	r.authedMut("POST", "/api/v1/crews/{crewId}/rebuild", roleCreate, provisioning.ProvisionRebuild)
	r.authedMut("POST", "/api/v1/crews/{crewId}/restart-agents", roleCreate, provisioning.RestartCrewAgents)

	// Devcontainer image cache management (GC)
	r.mux.Handle("GET /api/v1/cache/images", authed(wsCtx(http.HandlerFunc(provisioning.CacheList))))
	r.authedMut("DELETE", "/api/v1/cache/images/{tag}", roleManage, provisioning.CacheDelete)

	return provisioning
}
