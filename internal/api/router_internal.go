package api

// Sidecar IPC routes — every endpoint under /api/v1/internal/* lives
// here. Auth is the shared X-Internal-Token attached by the sidecar
// (see internal.requireInternal). The companion public routes for
// pipelines / assignments / queries / port-expose are registered in
// their respective domain files and the constructed handlers are
// passed in so both sides use the same instance.

import (
	"net/http"

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
	internal.SetJournal(r.Journal())
	// Attach the sleep-time consolidator hook (PRD §8.1). nil is a
	// no-op; SetPostRunTrigger no-ops on a nil receiver hook.
	internal.SetPostRunTrigger(oh.postRunTrigger)
	internalAuth := internal.requireInternal
	// Pipeline save — sidecar→main forward. Trust comes from
	// X-Internal-Token (sidecar attaches it via proxyIPCJSON);
	// regular JWT-authed users hit the public surface instead.
	r.mux.Handle("POST /api/v1/internal/pipelines/save", internalAuth(http.HandlerFunc(pipes.InternalSave)))
	r.mux.Handle("GET /api/v1/internal/credentials", internalAuth(http.HandlerFunc(internal.ListCredentials)))
	r.mux.Handle("PATCH /api/v1/internal/credentials/{credentialId}", internalAuth(http.HandlerFunc(internal.UpdateCredentialStatus)))
	r.mux.Handle("POST /api/v1/internal/chats", internalAuth(http.HandlerFunc(internal.CreateChat)))
	r.mux.Handle("GET /api/v1/internal/chats/{chatId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveChat)))
	r.mux.Handle("GET /api/v1/internal/agents/{agentId}/resolve", internalAuth(http.HandlerFunc(internal.ResolveAgent)))
	r.mux.Handle("GET /api/v1/internal/agents/{agentId}/webhook-secret", internalAuth(http.HandlerFunc(internal.GetWebhookSecret)))
	r.mux.Handle("POST /api/v1/internal/runs", internalAuth(http.HandlerFunc(internal.CreateRun)))
	r.mux.Handle("PATCH /api/v1/internal/runs/{runId}", internalAuth(http.HandlerFunc(internal.UpdateRun)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/message-count", internalAuth(http.HandlerFunc(internal.IncrementMessageCount)))
	r.mux.Handle("PATCH /api/v1/internal/chats/{chatId}/title", internalAuth(http.HandlerFunc(internal.UpdateChatTitle)))
	r.mux.Handle("GET /api/v1/internal/crews", internalAuth(http.HandlerFunc(internal.ListCrews)))
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
	r.mux.Handle("POST /api/v1/internal/missions", internalAuth(http.HandlerFunc(internalMissions.Create)))
	r.mux.Handle("GET /api/v1/internal/missions/{missionId}", internalAuth(http.HandlerFunc(internalMissions.Get)))
	r.mux.Handle("POST /api/v1/internal/missions/{missionId}/start", internalAuth(http.HandlerFunc(internalMissions.Start)))

	// Internal issue routes (called by sidecar on behalf of agents)
	internalIssues := NewInternalIssueHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/internal/issues", internalAuth(http.HandlerFunc(internalIssues.List)))
	r.mux.Handle("GET /api/v1/internal/issues/{identifier}", internalAuth(http.HandlerFunc(internalIssues.Get)))
	r.mux.Handle("POST /api/v1/internal/issues", internalAuth(http.HandlerFunc(internalIssues.Create)))
	r.mux.Handle("PATCH /api/v1/internal/issues/{identifier}", internalAuth(http.HandlerFunc(internalIssues.UpdateStatus)))
	r.mux.Handle("POST /api/v1/internal/issues/{identifier}/comments", internalAuth(http.HandlerFunc(internalIssues.CreateComment)))

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
	kp2 := NewKeeperPhase2Handler(
		r.db, r.internalToken, r.PolicyResolver(),
		r.skillReviewEval, r.behaviorEval, r.memHealthEval, r.negativeEval,
		r.logger,
	)
	r.mux.Handle("POST /api/v1/internal/keeper/skill-review", internalAuth(http.HandlerFunc(kp2.HandleSkillReview)))
	r.mux.Handle("POST /api/v1/internal/keeper/behavior", internalAuth(http.HandlerFunc(kp2.HandleBehavior)))
	r.mux.Handle("POST /api/v1/internal/keeper/memory-health", internalAuth(http.HandlerFunc(kp2.HandleMemoryHealth)))
	r.mux.Handle("POST /api/v1/internal/keeper/negative-learning", internalAuth(http.HandlerFunc(kp2.HandleNegativeLearning)))

	// Sidecar IPC — the agent-initiated port-expose request flow.
	// PortExposeHandler instance comes from registerOrchestrationRoutes
	// so the public capability + revoke endpoints share its registry
	// state with this internal create call.
	r.mux.Handle("POST /api/v1/internal/port-expose", internalAuth(http.HandlerFunc(oh.portExposeH.RequestExpose)))
}
