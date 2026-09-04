package api

// Sidecar IPC routes — every endpoint under /api/v1/internal/* lives
// here. Auth is the shared X-Internal-Token attached by the sidecar
// (see internal.requireInternal). The companion public routes for
// pipelines / assignments / queries / port-expose are registered in
// their respective domain files and the constructed handlers are
// passed in so both sides use the same instance.

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// registerInternalRoutes wires every /api/v1/internal/* endpoint
// plus the keeper internal surface and the cross-crew messaging
// handler used by sidecars.
//
// pipes, assign, queries, and portExposeH are constructed in their
// domain files and shared so the public + internal surfaces dispatch
// to the same handler instance (matters for in-memory state like
// pending escalations).
func (r *Router) registerInternalRoutes(pipes *PipelineHandler, oh orchestrationHandlers) {
	// Internal routes (for crewshipd IPC, X-Internal-Token auth)
	internal := NewInternalHandler(r.db, r.internalToken, r.logger)
	if r.hub != nil {
		internal.SetHub(r.hub)
	}
	if r.keeperConfig != nil && r.keeperConfig.Enabled {
		internal.SetKeeperEnabled(true)
	}
	// Revoke → remove file-based /secrets from running containers when the
	// sidecar reports status REVOKED (#814 parity with the public DELETE
	// handler). nil (no docker) makes the reconcile a no-op.
	internal.SetContainer(r.keeperContainer)
	// Default-connector behaviour flag (COMPOSIO_DEFAULT_CONNECTOR). Threaded
	// from the same Composio config the ComposioHandler uses so the runtime
	// resolver and the operator surface agree on whether it's armed.
	if r.composioConfig != nil {
		internal.SetComposioDefaultConnector(r.composioConfig.DefaultConnector, r.composioConfig.BaseURL)
	}
	internal.SetJournal(r.Journal())
	// Derive approval_mode from the crew autonomy policy in the resolve
	// response so the request-builder can revive the harbormaster HITL gate
	// on every dispatch path (#810). Shares the Router's cached resolver.
	internal.SetPolicyResolver(r.PolicyResolver())
	// Attach the sleep-time consolidator hook (PRD §8.1). nil is a
	// no-op; SetPostRunTrigger no-ops on a nil receiver hook.
	internal.SetPostRunTrigger(oh.postRunTrigger)
	// Post-run outcome verdict (#1403): the Router's resolver is handed
	// over, not its result — it is shared with every
	// pipeline.NewWiredExecutor call site (see Router.RunVerdict) and
	// caches the built provider per wiring, so a run_summary override
	// applies to the next verdict rather than the next boot (#1556).
	internal.SetRunVerdict(r.RunVerdict)
	// Resolve once here for the log line only. Nothing keeps the result — the
	// point of the resolver is that nobody does — but an instance whose
	// run_summary slot cannot be built should say so at startup rather than
	// first admitting it after someone's run finished with no verdict.
	r.RunVerdict()
	// Retain a reference so graceful shutdown can drain this handler's
	// in-flight post-run verdict goroutines before the journal closes
	// (Router.DrainVerdicts). Otherwise `internal` is local to this func.
	r.internalHandler = internal
	internalAuth := internal.requireInternal
	// Pipeline save — sidecar→main forward. Trust comes from
	// X-Internal-Token (sidecar attaches it via proxyIPCJSON);
	// regular JWT-authed users hit the public surface instead.
	// Pipeline test_run — sidecar→main forward (Step 1 of agent save).
	// Same internal-token trust as save; the public test_run is JWT-authed
	// and the sidecar only carries the internal token.
	r.mux.Handle("POST /api/v1/internal/pipelines/test_run", internalAuth(http.HandlerFunc(pipes.InternalTestRun)))
	r.mux.Handle("POST /api/v1/internal/pipelines/save", internalAuth(http.HandlerFunc(pipes.InternalSave)))
	// Routine RUN — sidecar→main forward for the run_routine MCP tool. The
	// public run route is JWT-authed and rejects the internal token, so agents
	// invoke saved routines through this internal surface (workspace + invoker
	// identity injected from IPC, same trust boundary as save).
	r.mux.Handle("POST /api/v1/internal/pipelines/run", internalAuth(http.HandlerFunc(pipes.InternalRun)))
	r.mux.Handle("GET /api/v1/internal/credentials", internalAuth(http.HandlerFunc(internal.ListCredentials)))
	r.mux.Handle("PATCH /api/v1/internal/credentials/{credentialId}", internalAuth(http.HandlerFunc(internal.UpdateCredentialStatus)))
	r.mux.Handle("POST /api/v1/internal/chats", internalAuth(http.HandlerFunc(internal.CreateChat)))
	r.mux.Handle("GET /api/v1/internal/chats/{chatId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveChat)))
	r.mux.Handle("GET /api/v1/internal/agents/{agentId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveAgent)))
	// GET .../agents/{agentId}/webhook-secret removed (#999) — the webhook
	// handler reads the secret from its local DB; plaintext never over IPC.
	r.mux.Handle("POST /api/v1/internal/runs", internalAuth(http.HandlerFunc(internal.CreateRun)))
	r.mux.Handle("PATCH /api/v1/internal/runs/{runId}", internalAuth(http.HandlerFunc(internal.UpdateRun)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/message-count", internalAuth(http.HandlerFunc(internal.IncrementMessageCount)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/title", internalAuth(http.HandlerFunc(internal.UpdateChatTitle)))
	r.mux.Handle("GET /api/v1/internal/crews", internalAuth(http.HandlerFunc(internal.ListCrews)))
	r.mux.Handle("GET /api/v1/internal/workspace/overview", internalAuth(http.HandlerFunc(internal.WorkspaceOverview)))
	r.mux.Handle("POST /api/v1/internal/crews", internalAuth(http.HandlerFunc(internal.CreateCrew)))
	r.mux.Handle("POST /api/v1/internal/agents", internalAuth(http.HandlerFunc(internal.CreateAgent)))
	// PR-D F5: LEAD-initiated ephemeral hire. Sidecar /spawn proxies
	// here; the adapter injects workspace + MANAGER role into the
	// context so the public Hire handler's RBAC + policy gate path
	// runs unchanged. nil-safe when agentHandler isn't wired (early
	// init / test routers); the adapter returns 500 in that case.
	if r.agentHandler != nil {
		hireAdapter := NewHireInternalAdapter(r.agentHandler)
		r.mux.Handle("POST /api/v1/internal/agents/hire", internalAuth(http.HandlerFunc(hireAdapter.Hire)))
	}
	// PRD-SLASH-CAPABILITIES-2026 §6.4 — three internal mirrors so
	// the sidecar's slash-action routes (commit 5) have a backend
	// surface to proxy into. Each adapter injects workspace +
	// MANAGER role; the credentials adapter additionally requires
	// X-Caller-User-Id (autonomous credential mutation is rejected
	// at the adapter boundary). nil-safe registration mirrors the
	// hireAdapter pattern above — test routers that don't construct
	// the parent handler skip the mirror entirely.
	if pipes != nil {
		routineAdapter := NewRoutineInternalAdapter(pipes)
		// #1768: the autonomy gate. Without this the adapter falls back to
		// the conservative guided default (every schedule staged disabled),
		// which is safe but not what an operator on trusted/full configured.
		routineAdapter.SetAutonomyGate(r.PolicyResolver(), r.Journal())
		r.mux.Handle("POST /api/v1/internal/routines/schedules", internalAuth(http.HandlerFunc(routineAdapter.CreateSchedule)))

		// The two READ tools (#1763). list_routines and
		// discover_capabilities forwarded to the public JWT-authed
		// routes while the sidecar carries only X-Internal-Token, so
		// both answered 401 and an agent authoring a routine could see
		// neither the crew's reach nor the existing library. Their own
		// CrewHandler is built here rather than threaded through the
		// signature — it is stateless over (db, logger), the same pair
		// router_crews.go constructs it from.
		readAdapter := NewPipelineReadInternalAdapter(pipes, NewCrewHandler(r.db, r.logger))
		// internalWsCtx, not a hand-rolled query read: it requires
		// workspace_id, refuses one that disagrees with the token's
		// binding, and injects the context — the middle step being the
		// one a hand-rolled version forgets.
		r.mux.Handle("GET /api/v1/internal/pipelines",
			internalAuth(internalWsCtx(http.HandlerFunc(readAdapter.ListPipelines))))
		r.mux.Handle("GET /api/v1/internal/crews/{crewId}/capabilities",
			internalAuth(internalWsCtx(http.HandlerFunc(readAdapter.CrewCapabilities))))
	}
	if r.skillGenHandler != nil {
		skillAdapter := NewSkillInternalAdapter(r.skillGenHandler)
		// #1768: the held arm stages the generated SKILL.md into the crew's
		// .proposed queue, so the adapter needs the proposed handler as well
		// as the resolver. A nil skillPropHandler leaves the held arm with
		// nowhere to stage and it fails closed (403) rather than registering.
		skillAdapter.SetAutonomyGate(r.PolicyResolver(), r.skillPropHandler)
		r.mux.Handle("POST /api/v1/internal/skills/generate", internalAuth(http.HandlerFunc(skillAdapter.Generate)))
	}
	if r.skillPropHandler != nil {
		r.skillPropHandler.SetPolicyResolver(r.PolicyResolver())
		authorAdapter := NewSkillAuthorAdapter(r.skillPropHandler)
		r.mux.Handle("POST /api/v1/internal/skills/author", internalAuth(http.HandlerFunc(authorAdapter.Author)))
	}
	if r.credentialHandler != nil {
		credAdapter := NewCredentialInternalAdapter(r.credentialHandler)
		r.mux.Handle("POST /api/v1/internal/credentials", internalAuth(http.HandlerFunc(credAdapter.Create)))
		r.mux.Handle("POST /api/v1/internal/credentials/{credentialId}/rotate", internalAuth(http.HandlerFunc(credAdapter.Rotate)))
	}
	// Hybrid memory search — sidecar→main forward (#1348). The sidecar
	// forwards the ACTING agent's slug in X-Acting-Agent-Slug; the handler
	// resolves it strictly inside the workspace/crew the internal token is
	// bound to, so own-scope recall narrows to the caller instead of
	// collapsing every shared-container sibling onto the token identity.
	// nil-safe: test routers that skip orchestration routes have no
	// shared handler instance to mount.
	if r.hybridSearchHandler != nil {
		r.mux.Handle("POST /api/v1/internal/memory/search/hybrid", internalAuth(http.HandlerFunc(r.hybridSearchHandler.SearchInternal)))
	}
	r.mux.Handle("GET /api/v1/internal/crew-connections", internalAuth(http.HandlerFunc(internal.ListCrewConnections)))
	r.mux.Handle("POST /api/v1/internal/mcp-tool-calls", internalAuth(http.HandlerFunc(internal.RecordMCPToolCall)))
	// Sidecar-emitted Crow's Nest journal events (network.egress, file.written).
	// Handler enforces a strict entry-type allowlist so agents can't fabricate
	// assignment.completed / approval.granted rows via the sidecar.
	r.mux.Handle("POST /api/v1/internal/journal/emit", internalAuth(http.HandlerFunc(r.handleSidecarEmit)))
	// Sidecar-emitted cost ledger rows. Sidecar parses LLM provider responses
	// (Anthropic/OpenAI/Google) for token usage + rate-limit headers, then
	// POSTs here so paymaster.Record can write the row + emit llm.call /
	// cost.incurred / budget.* journal entries on the trusted plane.
	r.mux.Handle("POST /api/v1/internal/cost/record", internalAuth(http.HandlerFunc(r.handleSidecarCostRecord)))

	// Cross-crew messaging and file sharing (called by sidecar)
	crewMsg := NewCrewMessagingHandler(r.db, r.storagePath, r.logger)
	// Agent-initiated notifications. An agent may send to a channel a human
	// explicitly paired it with — default-deny, per-agent rate limited, and
	// scrubbed on the way out. See internal_notifications.go.
	agentNotify := NewAgentNotifyHandler(r.db, mailer.NewFromEnv(), r.Journal(), r.logger)
	r.mux.Handle("GET /api/v1/internal/notifications/channels", internalAuth(http.HandlerFunc(agentNotify.ListChannels)))
	r.mux.Handle("POST /api/v1/internal/notifications/send", internalAuth(http.HandlerFunc(agentNotify.Send)))

	r.mux.Handle("POST /api/v1/internal/crew-messages", internalAuth(http.HandlerFunc(crewMsg.SendMessage)))
	r.mux.Handle("GET /api/v1/internal/crew-messages", internalAuth(http.HandlerFunc(crewMsg.ListMessages)))
	r.mux.Handle("GET /api/v1/internal/crew-files/{crewId}", internalAuth(http.HandlerFunc(crewMsg.ReadFile)))
	r.mux.Handle("POST /api/v1/internal/crew-files/{crewId}", internalAuth(http.HandlerFunc(crewMsg.WriteFile)))

	// Assignment routes (internal auth, called by sidecar on behalf of lead agents).
	// AssignmentHandler instance comes from registerOrchestrationRoutes so the
	// public list endpoint shares state with the internal create/get.
	r.mux.Handle("POST /api/v1/internal/assignments", internalAuth(http.HandlerFunc(oh.assign.Create)))
	r.mux.Handle("GET /api/v1/internal/assignments/{assignmentId}", internalAuth(http.HandlerFunc(oh.assign.Get)))

	// Internal mission routes (called by sidecar on behalf of lead agents)
	var missionEngineForInternal *orchestrator.MissionEngine
	if mc, ok := r.missionCallback.(*orchestrator.MissionEngine); ok {
		missionEngineForInternal = mc
	}
	internalMissions := NewInternalMissionHandler(r.db, r.hub, missionEngineForInternal, r.logger)
	// #1768: mission_create gate + the Start-side hold check.
	internalMissions.SetAutonomyGate(r.PolicyResolver(), r.Journal())
	r.mux.Handle("POST /api/v1/internal/missions", internalAuth(http.HandlerFunc(internalMissions.Create)))
	r.mux.Handle("GET /api/v1/internal/missions/{missionId}", internalAuth(http.HandlerFunc(internalMissions.Get)))
	r.mux.Handle("POST /api/v1/internal/missions/{missionId}/start", internalAuth(http.HandlerFunc(internalMissions.Start)))

	// Internal issue routes (called by sidecar on behalf of agents)
	internalIssues := NewInternalIssueHandler(r.db, r.hub, r.logger)
	// Same emitter the public issue handler gets (router_orchestration.go).
	// Without it an agent's issue change wrote a mission_activity row and
	// nothing else, and since notifications route per journal entry type
	// (internal/notifyroute), nothing an agent did to the board could ever
	// notify anyone — #1768 F1.
	internalIssues.SetJournal(r.Journal())
	// #1768 item 3: an @mention in an agent's comment wakes the mentioned
	// agent through the /assign chokepoint, so it inherits the delegation
	// depth + fan-out caps instead of getting a cap of its own. Same
	// AssignmentHandler instance the sidecar's /assign uses.
	if oh.assign != nil {
		internalIssues.SetMentionDispatcher(oh.assign)
	}
	r.mux.Handle("GET /api/v1/internal/issues", internalAuth(http.HandlerFunc(internalIssues.List)))
	r.mux.Handle("GET /api/v1/internal/issues/{identifier}", internalAuth(http.HandlerFunc(internalIssues.Get)))
	r.mux.Handle("POST /api/v1/internal/issues", internalAuth(http.HandlerFunc(internalIssues.Create)))
	r.mux.Handle("PATCH /api/v1/internal/issues/{identifier}", internalAuth(http.HandlerFunc(internalIssues.UpdateStatus)))
	r.mux.Handle("POST /api/v1/internal/issues/{identifier}/comments", internalAuth(http.HandlerFunc(internalIssues.CreateComment)))
	r.mux.Handle("GET /api/v1/internal/issues/{identifier}/comments", internalAuth(http.HandlerFunc(internalIssues.ListComments)))
	r.mux.Handle("POST /api/v1/internal/issues/{identifier}/relations", internalAuth(http.HandlerFunc(internalIssues.CreateRelation)))

	// Internal attachment routes (#1768 item 7) — the half that makes the
	// feature real. A person drops a stack trace on an issue so the agent
	// working it can read the stack trace; before these three routes the agent's
	// issue surface could see a title, a description, comments and linked pull
	// requests, and no files at all.
	//
	// The handler WRAPS the public one (registerOrchestrationRoutes, retained on
	// the Router) rather than constructing a second: the filename sanitisation,
	// the content-type allowlist, the content addressing and the de-duplication
	// have to be the same on both doors, and two handlers is how they stop
	// being. nil is the test-router case, where the orchestration group did not
	// run — skipping is right there, since a second instance built here would be
	// the exact drift the sharing exists to prevent.
	if r.attachmentHandler != nil {
		internalAttach := NewInternalAttachmentHandler(r.attachmentHandler)
		r.mux.Handle("GET /api/v1/internal/issues/{identifier}/attachments", internalAuth(http.HandlerFunc(internalAttach.List)))
		r.mux.Handle("GET /api/v1/internal/issues/{identifier}/attachments/{attachmentId}", internalAuth(http.HandlerFunc(internalAttach.Read)))
		r.mux.Handle("POST /api/v1/internal/issues/{identifier}/attachments", internalAuth(http.HandlerFunc(internalAttach.Attach)))
	}

	// Query routes (peer-to-peer communication, standup summaries, escalations).
	// Internal-auth side; public counterparts are registered in
	// router_orchestration.go using the same QueryHandler instance.
	r.mux.Handle("POST /api/v1/internal/queries", internalAuth(http.HandlerFunc(oh.queries.Create)))
	r.mux.Handle("GET /api/v1/internal/standup", internalAuth(http.HandlerFunc(oh.queries.Standup)))
	r.mux.Handle("POST /api/v1/internal/escalations", internalAuth(http.HandlerFunc(oh.queries.CreateEscalation)))
	r.mux.Handle("GET /api/v1/internal/escalations/{escalationId}/wait", internalAuth(http.HandlerFunc(oh.queries.WaitForEscalationResponse)))
	r.mux.Handle("POST /api/v1/internal/report-confidence", internalAuth(http.HandlerFunc(oh.queries.ReportConfidence)))

	// Keeper — credential access control (internal auth)
	keeperH := NewKeeperHandler(r.db, r.internalToken, r.keeperGK, r.logger).
		WithSecrets(r.keeperSecrets).
		WithContainer(r.keeperContainer).
		WithConversations(r.keeperConvReader)
	// The judge profile decides whether the credential path gathers evidence,
	// enforces the binding hard gate and budgets the prompt. Without this line
	// every one of those is off while the admin API reports it on — see
	// TestRouter_KeeperHandlerReceivesTheJudgeProfile.
	keeperH.SetJudgeConfig(r.keeperSettings)
	r.keeperHandler = keeperH

	// An ADMIN route, registered here rather than in router_admin.go because that
	// group runs BEFORE this one and the handler does not exist yet when it does.
	// Deliberately the same handler: an operator asking "how would my judge rule
	// on this?" must travel the path an agent's request travels, including the
	// tier floors, the audit row, the inbox item and the health record. A second
	// handler would drift into answering a different question — which is the
	// mistake this package already made twice with the think and format flags.
	r.authedMut("POST", "/api/v1/admin/keeper/ask", roleManage, keeperH.HandleAsk)
	r.authedMut("POST", "/api/v1/admin/keeper/requests/{requestId}/resolve", roleManage, keeperH.HandleResolve)
	// The keeper's share of a workspace-contents wipe. keeper_requests has no
	// workspace_id and no cascade from agents, so `seed --nuke` could not reach it
	// and 115 rows survived one on dev2 — each carrying an intent and the
	// conversation the judge was shown.
	r.authedMut("DELETE", "/api/v1/admin/keeper/requests", roleManage, keeperH.HandlePurge)
	if r.hub != nil {
		keeperH.WithBroadcaster(&keeperWSBroadcaster{hub: r.hub})
	}
	// Mirror keeper.request / keeper.decision into the unified Crew
	// Journal so credential-access events surface in the Timeline
	// alongside operational events. The keeper_requests table remains
	// the source of truth for the dedicated keeper UI; this is purely
	// additive observability.
	if r.journal != nil {
		keeperH.SetJournal(r.journal)
	}
	r.mux.Handle("POST /api/v1/internal/keeper/request", internalAuth(http.HandlerFunc(keeperH.HandleRequest)))
	r.mux.Handle("GET /api/v1/internal/keeper/request/{requestId}", internalAuth(http.HandlerFunc(keeperH.GetRequest)))
	r.mux.Handle("POST /api/v1/internal/keeper/execute", internalAuth(http.HandlerFunc(keeperH.HandleExecute)))

	// Keeper Phase 2 (PR-C / PRD §6 F4). The four endpoints below are
	// always registered so callers get a deterministic 503 ("evaluator
	// not configured") instead of a 404 when the aux-LLM wiring is
	// partial — easier to debug than missing route surface.
	// Evaluators are nil until the server bootstrap wires them via
	// Router.SetKeeperPhase2Evaluators (follow-up wire-up commit on
	// the server-startup side).
	//
	// Shared with the operator-facing manual-run route (#1555) via
	// keeperPhase2Handler() — one handler instance, so a manual run and a
	// scheduled/sidecar one cannot drift apart.
	kp2 := r.keeperPhase2Handler()
	// F4 Keeper Phase 2 endpoints — wrapped in BOTH internalAuth (sidecar
	// token gate) AND internalWsCtx (puts ?workspace_id= into request
	// context). Handlers depend on the context value to run
	// assertBodyWorkspaceMatchesCtx, the cross-tenant defense for the
	// case "internal-auth caller submits body.workspace_id for a tenant
	// they shouldn't be able to touch." Without internalWsCtx the gate
	// fires fail-closed on every call ("request context is missing
	// workspace_id") — the dead-code state that the round-8 fail-closed-
	// on-empty rewrite was supposed to alert on, except no operator was
	// hitting these routes (they're sidecar-only). Caught live on dev1
	// 2026-05-22 during the cross-tenant probe step of memory A/B audit.
	r.mux.Handle("POST /api/v1/internal/keeper/skill-review", internalAuth(internalWsCtx(http.HandlerFunc(kp2.HandleSkillReview))))
	r.mux.Handle("POST /api/v1/internal/keeper/behavior", internalAuth(internalWsCtx(http.HandlerFunc(kp2.HandleBehavior))))
	r.mux.Handle("POST /api/v1/internal/keeper/memory-health", internalAuth(internalWsCtx(http.HandlerFunc(kp2.HandleMemoryHealth))))
	r.mux.Handle("POST /api/v1/internal/keeper/negative-learning", internalAuth(internalWsCtx(http.HandlerFunc(kp2.HandleNegativeLearning))))

	// Pages — the routine write path (#1945). The far side of the `page.write`
	// crewship verb: a routine writes one panel's payload, bounded by the author
	// crew's autonomy level on the way out (policy.ActionPageWrite) and by the
	// panel's declared producer or an explicit produce grant on the way in.
	// Registered in pages_internal.go, which owns the handler and the fences.
	r.registerInternalPageRoutes(internalAuth)
	// Pages — agent-authored CREATE (the structure gap save_page closes).
	// Gated on policy.ActionPageCreate, not page_write's autonomy row — see
	// pages_internal_save.go's own header for why the two are separate.
	r.registerInternalPageSaveRoute(internalAuth)

	// Sidecar IPC — the agent-initiated port-expose request flow.
	// PortExposeHandler instance comes from registerOrchestrationRoutes
	// so the public capability + revoke endpoints share its registry
	// state with this internal create call.
	r.mux.Handle("POST /api/v1/internal/port-expose", internalAuth(http.HandlerFunc(oh.portExposeH.RequestExpose)))
}
