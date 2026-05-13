package api

// Orchestration-domain routes — the heart of the app's day-to-day
// surface: missions, issues, projects, milestones, notifications,
// runs, journal, cartographer, paymaster, approvals, inbox, eval,
// queries (peer-to-peer + escalations + activity feed), assignments,
// proxy/files, MCP audit + registry, OAuth provider flows, and the
// agent-initiated port-expose capability.
//
// Several handlers (assign, queries, portExposeH) are also used by
// router_internal.go for the sidecar IPC counterparts; they are
// returned via orchestrationHandlers so the internal registrar can
// re-use the same instance.

import (
	"context"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// orchestrationHandlers bundles the handlers constructed in
// registerOrchestrationRoutes that are also referenced by
// registerInternalRoutes. Keeping the bundle small (3 fields) avoids
// a parameter explosion on the internal registrar.
type orchestrationHandlers struct {
	assign      *AssignmentHandler
	queries     *QueryHandler
	portExposeH *PortExposeHandler
}

// registerOrchestrationRoutes wires the orchestration surface and
// returns the shared handlers required by registerInternalRoutes.
func (r *Router) registerOrchestrationRoutes() orchestrationHandlers {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// Missions
	var missionEngineForPublic *orchestrator.MissionEngine
	if r.missionCallback != nil {
		if mc, ok := r.missionCallback.(*orchestrator.MissionEngine); ok {
			missionEngineForPublic = mc
		}
	}
	missions := NewMissionHandler(r.db, r.hub, missionEngineForPublic, r.logger)
	r.mux.Handle("GET /api/v1/missions", authed(wsCtx(http.HandlerFunc(missions.ListAll))))
	r.mux.Handle("GET /api/v1/mission-metrics", authed(wsCtx(http.HandlerFunc(missions.Metrics))))
	metricsH := NewMetricsHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/metrics/timeseries", authed(wsCtx(http.HandlerFunc(metricsH.Timeseries))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.List))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.Create))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Get))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Delete))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/start", authed(wsCtx(http.HandlerFunc(missions.Start))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/restart", authed(wsCtx(http.HandlerFunc(missions.Restart))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/resume", authed(wsCtx(http.HandlerFunc(missions.Resume))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/clone", authed(wsCtx(http.HandlerFunc(missions.Clone))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/missions/{missionId}/tasks", authed(wsCtx(http.HandlerFunc(missions.CreateTask))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}", authed(wsCtx(http.HandlerFunc(missions.UpdateTask))))

	// Issues (Linear-like issue tracker)
	var issueStarter MissionStarter
	if missionEngineForPublic != nil {
		issueStarter = missionEngineForPublic
	}
	issues := NewIssueHandler(r.db, r.hub, issueStarter, r.logger)
	issues.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/issues", authed(wsCtx(http.HandlerFunc(issues.List))))
	r.mux.Handle("GET /api/v1/issues/{identifier}", authed(wsCtx(http.HandlerFunc(issues.GetByIdentifier))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/issues", authed(wsCtx(http.HandlerFunc(issues.Create))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}", authed(wsCtx(http.HandlerFunc(issues.Get))))
	r.mux.Handle("PATCH /api/v1/crews/{crewId}/issues/{identifier}", authed(wsCtx(http.HandlerFunc(issues.Update))))
	r.mux.Handle("DELETE /api/v1/crews/{crewId}/issues/{identifier}", authed(wsCtx(http.HandlerFunc(issues.Delete))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/issues/{identifier}/start", authed(wsCtx(http.HandlerFunc(issues.Start))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/issues/{identifier}/stop", authed(wsCtx(http.HandlerFunc(issues.Stop))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/issues/{identifier}/review", authed(wsCtx(http.HandlerFunc(issues.Review))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/activity", authed(wsCtx(http.HandlerFunc(issues.ListActivity))))
	// Labels
	r.mux.Handle("GET /api/v1/labels", authed(wsCtx(http.HandlerFunc(issues.ListLabels))))
	r.mux.Handle("POST /api/v1/labels", authed(wsCtx(http.HandlerFunc(issues.CreateLabel))))
	r.mux.Handle("PATCH /api/v1/labels/{labelId}", authed(wsCtx(http.HandlerFunc(issues.UpdateLabel))))
	r.mux.Handle("DELETE /api/v1/labels/{labelId}", authed(wsCtx(http.HandlerFunc(issues.DeleteLabel))))
	// Issue Comments
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/comments", authed(wsCtx(http.HandlerFunc(issues.ListComments))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/issues/{identifier}/comments", authed(wsCtx(http.HandlerFunc(issues.CreateComment))))
	// Issue Relations
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/relations", authed(wsCtx(http.HandlerFunc(issues.ListRelations))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/issues/{identifier}/relations", authed(wsCtx(http.HandlerFunc(issues.CreateRelation))))
	r.mux.Handle("DELETE /api/v1/relations/{relationId}", authed(wsCtx(http.HandlerFunc(issues.DeleteRelation))))
	// Projects
	projects := NewProjectHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/projects", authed(wsCtx(http.HandlerFunc(projects.List))))
	r.mux.Handle("POST /api/v1/projects", authed(wsCtx(http.HandlerFunc(projects.Create))))
	r.mux.Handle("GET /api/v1/projects/{projectId}", authed(wsCtx(http.HandlerFunc(projects.Get))))
	r.mux.Handle("PATCH /api/v1/projects/{projectId}", authed(wsCtx(http.HandlerFunc(projects.Update))))
	r.mux.Handle("DELETE /api/v1/projects/{projectId}", authed(wsCtx(http.HandlerFunc(projects.Delete))))
	r.mux.Handle("GET /api/v1/projects/{projectId}/stats", authed(wsCtx(http.HandlerFunc(projects.Stats))))

	// Milestones
	milestones := NewMilestoneHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/projects/{projectId}/milestones", authed(wsCtx(http.HandlerFunc(milestones.List))))
	r.mux.Handle("POST /api/v1/projects/{projectId}/milestones", authed(wsCtx(http.HandlerFunc(milestones.Create))))
	r.mux.Handle("PATCH /api/v1/milestones/{milestoneId}", authed(wsCtx(http.HandlerFunc(milestones.Update))))
	r.mux.Handle("DELETE /api/v1/milestones/{milestoneId}", authed(wsCtx(http.HandlerFunc(milestones.Delete))))
	// Notifications
	notifications := NewNotificationHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/notifications", authed(wsCtx(http.HandlerFunc(notifications.List))))
	r.mux.Handle("GET /api/v1/notifications/count", authed(wsCtx(http.HandlerFunc(notifications.Count))))
	r.mux.Handle("POST /api/v1/notifications/{notificationId}/read", authed(wsCtx(http.HandlerFunc(notifications.MarkRead))))
	r.mux.Handle("POST /api/v1/notifications/read-all", authed(wsCtx(http.HandlerFunc(notifications.MarkAllRead))))
	r.mux.Handle("DELETE /api/v1/notifications/{notificationId}", authed(wsCtx(http.HandlerFunc(notifications.Delete))))
	// Saved Views
	savedViews := NewSavedViewHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/saved-views", authed(wsCtx(http.HandlerFunc(savedViews.List))))
	r.mux.Handle("POST /api/v1/saved-views", authed(wsCtx(http.HandlerFunc(savedViews.Create))))
	r.mux.Handle("PATCH /api/v1/saved-views/{viewId}", authed(wsCtx(http.HandlerFunc(savedViews.Update))))
	r.mux.Handle("DELETE /api/v1/saved-views/{viewId}", authed(wsCtx(http.HandlerFunc(savedViews.Delete))))
	// Recurring Issues
	recurringIssues := NewRecurringIssueHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/recurring-issues", authed(wsCtx(http.HandlerFunc(recurringIssues.List))))
	r.mux.Handle("POST /api/v1/recurring-issues", authed(wsCtx(http.HandlerFunc(recurringIssues.Create))))
	r.mux.Handle("PATCH /api/v1/recurring-issues/{recurringId}", authed(wsCtx(http.HandlerFunc(recurringIssues.Update))))
	r.mux.Handle("DELETE /api/v1/recurring-issues/{recurringId}", authed(wsCtx(http.HandlerFunc(recurringIssues.Delete))))
	// Triage Rules
	triage := NewTriageHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/triage-rules", authed(wsCtx(http.HandlerFunc(triage.ListRules))))
	r.mux.Handle("POST /api/v1/triage-rules", authed(wsCtx(http.HandlerFunc(triage.CreateRule))))
	r.mux.Handle("PATCH /api/v1/triage-rules/{ruleId}", authed(wsCtx(http.HandlerFunc(triage.UpdateRule))))
	r.mux.Handle("DELETE /api/v1/triage-rules/{ruleId}", authed(wsCtx(http.HandlerFunc(triage.DeleteRule))))
	r.mux.Handle("POST /api/v1/triage/process", authed(wsCtx(http.HandlerFunc(triage.Process))))
	// Issue Bulk Operations
	r.mux.Handle("PATCH /api/v1/issues/bulk", authed(wsCtx(http.HandlerFunc(issues.BulkUpdate))))
	// Issue Sub-issues
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/subtasks", authed(wsCtx(http.HandlerFunc(issues.ListSubIssues))))

	// Runs (require workspace context)
	runs := NewRunHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/runs", authed(wsCtx(http.HandlerFunc(runs.List))))
	r.mux.Handle("GET /api/v1/runs/{id}", authed(wsCtx(http.HandlerFunc(runs.Get))))

	// Crew Journal: workspace-wide event stream. Reads only — writes are
	// internal via journal.Writer emits from handlers across the codebase.
	jh := NewJournalHandler(r.db, r.logger, r.Journal())
	r.mux.Handle("GET /api/v1/journal", authed(wsCtx(http.HandlerFunc(jh.List))))
	r.mux.Handle("GET /api/v1/journal/stream", authed(wsCtx(http.HandlerFunc(jh.Stream))))
	r.mux.Handle("GET /api/v1/journal/count", authed(wsCtx(http.HandlerFunc(jh.Count))))
	// Single-entry GET — needed by deep-links from the timeline (e.g. a
	// shared URL pointing at one keeper.decision row) and by the CLI's
	// `journal get <id>`. Workspace-scoped; cross-tenant IDs return 404
	// with the same shape as "not found".
	r.mux.Handle("GET /api/v1/journal/{id}", authed(wsCtx(http.HandlerFunc(jh.Get))))
	r.mux.Handle("POST /api/v1/journal/{id}/priority", authed(wsCtx(http.HandlerFunc(jh.SetPriority))))
	// Lookup table for journal-card enrichment (crew/agent/mission names
	// + crew icons & palette colors). Frontend fetches once on mount;
	// per-entry rendering reads from the cached map instead of running
	// a JOIN on every list/stream request.
	jlh := NewJournalLookupHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/journal/lookup", authed(wsCtx(http.HandlerFunc(jlh.Get))))

	// Cartographer: mission checkpoint / restore / fork API. The package
	// owns the row writes + journal emits; this handler is the HTTP
	// surface for the /missions/[id]/timeline UI.
	ch := NewCartographerHandler(r.db, r.logger)
	ch.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/missions/{missionId}/checkpoints", authed(wsCtx(http.HandlerFunc(ch.List))))
	r.mux.Handle("POST /api/v1/missions/{missionId}/checkpoints", authed(wsCtx(http.HandlerFunc(ch.Create))))
	r.mux.Handle("GET /api/v1/checkpoints/{id}", authed(wsCtx(http.HandlerFunc(ch.Get))))
	r.mux.Handle("POST /api/v1/checkpoints/{id}/restore", authed(wsCtx(http.HandlerFunc(ch.Restore))))
	r.mux.Handle("POST /api/v1/checkpoints/{id}/fork", authed(wsCtx(http.HandlerFunc(ch.Fork))))
	r.mux.Handle("DELETE /api/v1/checkpoints/{id}", authed(wsCtx(http.HandlerFunc(ch.Delete))))

	// Paymaster: cost + budget read endpoints backed by the cost_ledger
	// rollup queries. Writes to the ledger happen inside the LLM
	// middleware chain, not through this handler.
	ph := NewPaymasterHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/paymaster/spend/by-crew", authed(wsCtx(http.HandlerFunc(ph.SpendByCrew))))
	r.mux.Handle("GET /api/v1/paymaster/spend/by-agent/{crewId}", authed(wsCtx(http.HandlerFunc(ph.SpendByAgent))))
	r.mux.Handle("GET /api/v1/paymaster/spend/by-mission/{missionId}", authed(wsCtx(http.HandlerFunc(ph.SpendByMission))))
	r.mux.Handle("GET /api/v1/paymaster/top-spenders", authed(wsCtx(http.HandlerFunc(ph.TopSpenders))))
	r.mux.Handle("GET /api/v1/paymaster/subscriptions", authed(wsCtx(http.HandlerFunc(ph.SubscriptionUsage))))

	// Harbor Master: HITL approvals inbox. Enqueue side runs inside
	// the orchestrator's gate; this handler is list + decide for humans.
	ah := NewApprovalsHandler(r.db, r.logger, r.Journal())
	r.mux.Handle("GET /api/v1/approvals", authed(wsCtx(http.HandlerFunc(ah.List))))
	r.mux.Handle("GET /api/v1/approvals/{id}", authed(wsCtx(http.HandlerFunc(ah.Get))))
	r.mux.Handle("POST /api/v1/approvals/{id}/decide", authed(wsCtx(http.HandlerFunc(ah.Decide))))
	r.mux.Handle("POST /api/v1/approvals/reset-auto-tuning", authed(wsCtx(http.HandlerFunc(ah.ResetAutoTuning))))

	// Unified Inbox — the canonical "stuff that needs the human"
	// surface. Backed by inbox_items (migration v85). Source-of-
	// truth handlers (waitpoints, escalations, run failures) write
	// through to this table so the bell + /inbox page render from
	// one query instead of fanning out to four.
	ih := NewInboxHandler(r.db, r.logger, r.hub)
	r.mux.Handle("GET /api/v1/inbox", authed(wsCtx(http.HandlerFunc(ih.List))))
	r.mux.Handle("GET /api/v1/inbox/count", authed(wsCtx(http.HandlerFunc(ih.UnreadCount))))
	r.mux.Handle("PATCH /api/v1/inbox/{id}", authed(wsCtx(http.HandlerFunc(ih.PatchState))))

	// Memory health dashboard — 5-metric score with per-crew scope.
	// Read-only; available to every workspace member because the
	// output is aggregate counts and ratios, no raw entry content.
	mhh := NewMemoryHealthHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/memory/health", authed(wsCtx(http.HandlerFunc(mhh.Get))))

	// Agent inbox: consolidated "waiting on this agent" payload for the
	// Crews right-panel inbox view. One round-trip replaces four parallel
	// fetches (approvals + assignments + escalations + peer messages).
	aih := NewAgentInboxHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/agents/{agentId}/inbox", authed(wsCtx(http.HandlerFunc(aih.Handle))))

	// User preferences: generic key-value store for per-user UI settings
	// (panel sizes, density, last-opened tabs, …). Migration v58 created
	// the underlying table; values are arbitrary JSON owned by the FE per
	// key. Only the authenticated user can read/write their own row set.
	uph := NewUserPreferencesHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/me/preferences", authed(http.HandlerFunc(uph.List)))
	r.mux.Handle("PUT /api/v1/me/preferences/{key}", authed(http.HandlerFunc(uph.Set)))
	r.mux.Handle("DELETE /api/v1/me/preferences/{key}", authed(http.HandlerFunc(uph.Delete)))

	// Message reactions: per-(chat, message, emoji, user) emoji react with
	// idempotent INSERT OR IGNORE. Migration v57 created the underlying
	// table; endpoints are scoped via chats.workspace_id so cross-tenant
	// reads/writes return 404.
	mrh := NewMessageReactionsHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/chats/{chatId}/messages/{messageId}/reactions",
		authed(http.HandlerFunc(mrh.List)))
	r.mux.Handle("POST /api/v1/chats/{chatId}/messages/{messageId}/reactions",
		authed(http.HandlerFunc(mrh.Add)))
	r.mux.Handle("DELETE /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}",
		authed(http.HandlerFunc(mrh.Remove)))

	// Hooks registry: lifecycle intercepts. List is available to every
	// workspace member for auditability; enable/disable is OWNER/ADMIN
	// only because flipping a hook can invoke shell commands.
	hh := NewHooksHandler(r.db, r.logger)
	hh.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/hooks", authed(wsCtx(http.HandlerFunc(hh.List))))
	r.mux.Handle("POST /api/v1/hooks/{id}/enable", authed(wsCtx(http.HandlerFunc(hh.Enable))))
	r.mux.Handle("POST /api/v1/hooks/{id}/disable", authed(wsCtx(http.HandlerFunc(hh.Disable))))

	// Watch Roster: per-workspace presence snapshot. Read-only — agent
	// runtime owns status, so there's no POST here.
	preH := NewPresenceHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/presence/roster", authed(wsCtx(http.HandlerFunc(preH.Roster))))

	// Consolidate: manual trigger for the memory-consolidation worker.
	// Lock is per-workspace + kept inside the handler so two workspaces
	// don't serialise through each other.
	conH := NewConsolidateHandler(r.db, r.logger)
	conH.SetJournal(r.Journal())
	conH.SetConsolidator(r.consolidator)
	conH.SetMemoryRoot(r.consolidateMemoryRoot)
	r.mux.Handle("POST /api/v1/consolidate/run", authed(wsCtx(http.HandlerFunc(conH.Run))))

	// Quartermaster eval: mission replay + regression + list. Both
	// mutating calls run in a goroutine and return 202 with a run_id
	// the caller can later poll for in the list endpoint.
	evH := NewEvalHandler(r.db, r.logger)
	evH.SetJournal(r.Journal())
	r.mux.Handle("POST /api/v1/eval/replay", authed(wsCtx(http.HandlerFunc(evH.Replay))))
	r.mux.Handle("POST /api/v1/eval/regression", authed(wsCtx(http.HandlerFunc(evH.Regression))))
	r.mux.Handle("GET /api/v1/eval/runs", authed(wsCtx(http.HandlerFunc(evH.ListRuns))))

	// MCP tool call audit (require workspace context)
	mcpAudit := NewMCPAuditHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/mcp-tool-calls", authed(wsCtx(http.HandlerFunc(mcpAudit.List))))

	// MCP Registry (public browsing, auth required; manual sync requires workspace member)
	mcpRegistry := NewMCPRegistryHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/mcp-registry", authed(http.HandlerFunc(mcpRegistry.List)))
	r.mux.Handle("GET /api/v1/mcp-registry/search", authed(http.HandlerFunc(mcpRegistry.Search)))
	r.mux.Handle("POST /api/v1/mcp-registry/sync", authed(wsCtx(http.HandlerFunc(mcpRegistry.Sync))))

	// OAuth flow (auth required for initiate, callback is unauthenticated — uses state token)
	oauth := NewOAuthHandler(r.db, r.logger)
	if r.hub != nil {
		oauth.SetHub(r.hub)
	}
	r.mux.Handle("GET /api/v1/oauth/providers", authed(http.HandlerFunc(oauth.ListProviders)))
	r.mux.Handle("POST /api/v1/oauth/initiate", authed(wsCtx(http.HandlerFunc(oauth.Initiate))))
	r.mux.HandleFunc("GET /api/v1/oauth/callback", oauth.Callback) // No auth — uses state token
	r.mux.Handle("POST /api/v1/oauth/exchange", authed(wsCtx(http.HandlerFunc(oauth.Exchange))))
	r.mux.Handle("POST /api/v1/oauth/loopback", authed(wsCtx(http.HandlerFunc(oauth.Loopback))))
	r.mux.Handle("POST /api/v1/oauth/discover", authed(http.HandlerFunc(oauth.Discover)))
	r.mux.Handle("POST /api/v1/oauth/auto-connect", authed(wsCtx(http.HandlerFunc(oauth.AutoConnect))))

	// Crewshipd proxy + agent runtime routes (require IPC socket)
	socketPath := r.socketPath
	if socketPath == "" {
		socketPath = "/tmp/crewship.sock"
	}
	proxy := NewProxyHandler(r.db, r.logger, socketPath)
	r.mux.Handle("GET /api/v1/crewshipd", authed(wsCtx(http.HandlerFunc(proxy.CrewshipdHealth))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/debug", authed(wsCtx(http.HandlerFunc(proxy.AgentDebug))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/files", authed(wsCtx(http.HandlerFunc(proxy.AgentFiles))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/files/download", authed(wsCtx(http.HandlerFunc(proxy.AgentFileDownload))))
	r.mux.Handle("PUT /api/v1/agents/{agentId}/files/save", authed(wsCtx(http.HandlerFunc(proxy.AgentFileSave))))
	// Multipart upload tied to a (agent, chat) pair. Lands at
	// /output/<slug>/attachments/<chatId>/<filename> on the agent side.
	r.mux.Handle("POST /api/v1/agents/{agentId}/chats/{chatId}/attachments",
		authed(wsCtx(http.HandlerFunc(proxy.AgentChatAttachment))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/files", authed(wsCtx(http.HandlerFunc(proxy.CrewFiles))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/files/download", authed(wsCtx(http.HandlerFunc(proxy.CrewFileDownload))))
	r.mux.Handle("PUT /api/v1/crews/{crewId}/files/save", authed(wsCtx(http.HandlerFunc(proxy.CrewFileSave))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/container-files", authed(wsCtx(http.HandlerFunc(proxy.AgentContainerFiles))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/git-log", authed(wsCtx(http.HandlerFunc(proxy.AgentGitLog))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/logs", authed(wsCtx(http.HandlerFunc(proxy.AgentLogs))))
	r.mux.Handle("POST /api/v1/agents/{agentId}/stop", authed(wsCtx(http.HandlerFunc(proxy.AgentStop))))
	r.mux.Handle("GET /api/v1/chats/{chatId}/messages", authed(http.HandlerFunc(proxy.ChatMessages)))

	// Assignment routes (internal auth, called by sidecar on behalf of lead agents).
	// AssignmentHandler is constructed here so the public list endpoint
	// below shares the same instance; the internal-side POST/GET are
	// registered in router_internal.go.
	assign := NewAssignmentHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	assign.SetJournal(r.Journal())
	if r.missionCallback != nil {
		assign.SetMissionCallback(r.missionCallback)
		// Wire AssignmentHandler as the TaskDispatcher so the MissionEngine
		// can dispatch tasks (including cross-crew) through the same code path.
		if me, ok := r.missionCallback.(*orchestrator.MissionEngine); ok {
			me.SetDispatcher(assign)
		}
	}
	// Crew assignments (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/assignments", authed(wsCtx(http.HandlerFunc(assign.List))))

	// Query routes (peer-to-peer communication, standup summaries, escalations).
	// Public side registered here; internal-auth POST endpoints live in
	// router_internal.go using the same instance.
	queries := NewQueryHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	queries.SetJournal(r.Journal())

	// Crew peer conversations, standup, and escalations (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/peer-conversations", authed(wsCtx(http.HandlerFunc(queries.ListPeerConversations))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/standup", authed(wsCtx(http.HandlerFunc(queries.Standup))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/escalations", authed(wsCtx(http.HandlerFunc(queries.ListEscalations))))
	r.mux.Handle("PATCH /api/v1/escalations/{escalationId}/resolve", authed(wsCtx(http.HandlerFunc(queries.ResolveEscalation))))

	// Workspace-wide escalation count (public, authenticated)
	r.mux.Handle("GET /api/v1/escalations/pending-count", authed(wsCtx(http.HandlerFunc(queries.PendingEscalationCount))))

	// Cross-crew activity feed (public, authenticated)
	r.mux.Handle("GET /api/v1/activity", authed(wsCtx(http.HandlerFunc(queries.ListAllActivity))))

	// Port exposures — agent-initiated reverse proxy for container ports.
	// MVP uses AllowAllPolicy; the registry + proxy route + CLI list/revoke
	// are all wired here in one place so swapping in a future ApprovalPolicy
	// only touches the policy argument below.
	r.portExposeRegistry = NewPortExposeRegistry(r.db, r.logger)
	// Rehydrate active exposures from DB so they survive crewshipd restarts,
	// then kick off the TTL purge goroutine. Errors are logged but don't
	// abort router setup — an empty registry is still functional.
	if err := r.portExposeRegistry.LoadFromDB(context.Background()); err != nil {
		r.logger.Warn("port expose registry: initial load failed", "error", err)
	}
	r.portExposeRegistry.StartPurger(30 * time.Second)

	peCfg := DefaultPortExposeConfig()
	if r.portExposePublicURL != "" {
		peCfg.PublicBaseURL = r.portExposePublicURL
	}
	if r.portExposeNetwork != "" {
		peCfg.NetworkName = r.portExposeNetwork
	}
	var peInspector DockerInspector
	if r.dockerClient != nil {
		dc := r.dockerClient
		peInspector = DockerInspectorFunc(func(ctx context.Context, id, network string) (string, error) {
			inspect, err := dc.ContainerInspect(ctx, id)
			if err != nil {
				return "", err
			}
			if inspect.NetworkSettings == nil {
				return "", errPortExposeNoNetwork
			}
			ns, ok := inspect.NetworkSettings.Networks[network]
			if !ok || ns == nil || ns.IPAddress == "" {
				return "", errPortExposeNoNetwork
			}
			return ns.IPAddress, nil
		})
	}
	portExposeH := NewPortExposeHandler(r.db, r.portExposeRegistry, peInspector, AllowAllPolicy{}, r.hub, peCfg, r.logger)

	// Capability URL — no auth; the token IS the auth. Patterns without a
	// method prefix match every HTTP verb (Go 1.22+ ServeMux), so one handler
	// entry covers GET/POST/PUT/DELETE/PATCH/HEAD/OPTIONS for the reverse
	// proxy. Two variants because trailing-slash and bare-token forms are
	// both legitimate ways users / curl might hit the capability.
	r.mux.HandleFunc("/exposed/{token}/", portExposeH.ServeExposed)
	r.mux.HandleFunc("/exposed/{token}", portExposeH.ServeExposed)

	// User-facing audit + lifecycle.
	r.mux.Handle("GET /api/v1/crews/{crewId}/port-expose", authed(wsCtx(http.HandlerFunc(portExposeH.List))))
	r.mux.Handle("POST /api/v1/crews/{crewId}/port-expose/{id}/revoke", authed(wsCtx(http.HandlerFunc(portExposeH.Revoke))))

	// Webhooks (public, HMAC-secret protected)
	if r.orch != nil && r.keeperContainer != nil && r.logWriter != nil && r.internalToken != "" {
		// Use IPC resolver to talk to our own internal endpoints
		baseURL := r.internalBaseURL
		if baseURL == "" {
			baseURL = "http://localhost:8080"
		}
		resolver := chatbridge.NewIPCResolver(baseURL, r.internalToken, r.logger)
		wh := NewWebhookHandler(r.db, r.logger, resolver, r.orch, r.hub, r.keeperContainer, r.logWriter)
		r.mux.Handle("POST /api/v1/webhooks/{crewId}/{agentId}/trigger", http.HandlerFunc(wh.ServeHTTP))
	}

	return orchestrationHandlers{
		assign:      assign,
		queries:     queries,
		portExposeH: portExposeH,
	}
}
