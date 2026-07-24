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
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/moby/moby/client"
)

// orchestrationHandlers bundles the handlers constructed in
// registerOrchestrationRoutes that are also referenced by
// registerInternalRoutes. Keeping the bundle small (3 fields) avoids
// a parameter explosion on the internal registrar.
type orchestrationHandlers struct {
	assign      *AssignmentHandler
	queries     *QueryHandler
	portExposeH *PortExposeHandler
	// postRunTrigger is the sleep-time consolidator hook (PRD §8.1).
	// nil → no opportunistic firing, 6h cron stays as the safety net.
	// The internal registrar attaches this to InternalHandler so
	// UpdateRun can call it on every run.completed.
	postRunTrigger postRunTriggerHook
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
	missions.SetStoragePath(r.storagePath)
	r.mux.Handle("GET /api/v1/missions", authed(wsCtx(http.HandlerFunc(missions.ListAll))))
	r.mux.Handle("GET /api/v1/mission-metrics", authed(wsCtx(http.HandlerFunc(missions.Metrics))))
	metricsH := NewMetricsHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/metrics/timeseries", authed(wsCtx(http.HandlerFunc(metricsH.Timeseries))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions", authed(wsCtx(http.HandlerFunc(missions.List))))
	r.authedMut("POST", "/api/v1/crews/{crewId}/missions", roleCreate, missions.Create)
	r.mux.Handle("GET /api/v1/crews/{crewId}/missions/{missionId}", authed(wsCtx(http.HandlerFunc(missions.Get))))
	r.authedMut("PATCH", "/api/v1/crews/{crewId}/missions/{missionId}", roleCreate, missions.Update)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/missions/{missionId}", roleCreate, missions.Delete)
	r.authedMut("POST", "/api/v1/crews/{crewId}/missions/{missionId}/start", roleCreate, missions.Start)
	r.authedMut("POST", "/api/v1/crews/{crewId}/missions/{missionId}/restart", roleCreate, missions.Restart)
	r.authedMut("POST", "/api/v1/crews/{crewId}/missions/{missionId}/resume", roleCreate, missions.Resume)
	r.authedMut("POST", "/api/v1/crews/{crewId}/missions/{missionId}/clone", roleCreate, missions.Clone)
	r.authedMut("POST", "/api/v1/crews/{crewId}/missions/{missionId}/tasks", roleCreate, missions.CreateTask)
	r.authedMut("PATCH", "/api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}", roleCreate, missions.UpdateTask)

	// Issues (Linear-like issue tracker)
	var issueStarter MissionStarter
	if missionEngineForPublic != nil {
		issueStarter = missionEngineForPublic
	}
	issues := NewIssueHandler(r.db, r.hub, issueStarter, r.logger)
	issues.SetJournal(r.Journal())
	issues.SetStoragePath(r.storagePath)
	r.mux.Handle("GET /api/v1/issues", authed(wsCtx(http.HandlerFunc(issues.List))))
	r.mux.Handle("GET /api/v1/issues/{identifier}", authed(wsCtx(http.HandlerFunc(issues.GetByIdentifier))))
	r.authedMut("POST", "/api/v1/crews/{crewId}/issues", roleInline, issues.Create)
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}", authed(wsCtx(http.HandlerFunc(issues.Get))))
	r.authedMut("PATCH", "/api/v1/crews/{crewId}/issues/{identifier}", roleCreate, issues.Update)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/issues/{identifier}", roleCreate, issues.Delete)
	r.authedMut("POST", "/api/v1/crews/{crewId}/issues/{identifier}/start", roleCreate, issues.Start)
	r.authedMut("POST", "/api/v1/crews/{crewId}/issues/{identifier}/stop", roleCreate, issues.Stop)
	r.authedMut("POST", "/api/v1/crews/{crewId}/issues/{identifier}/review", roleCreate, issues.Review)
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/activity", authed(wsCtx(http.HandlerFunc(issues.ListActivity))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/runs", authed(wsCtx(http.HandlerFunc(issues.ListRuns))))
	// Labels
	r.mux.Handle("GET /api/v1/labels", authed(wsCtx(http.HandlerFunc(issues.ListLabels))))
	r.authedMut("POST", "/api/v1/labels", roleCreate, issues.CreateLabel)
	r.authedMut("PATCH", "/api/v1/labels/{labelId}", roleManage, issues.UpdateLabel)
	r.authedMut("DELETE", "/api/v1/labels/{labelId}", roleManage, issues.DeleteLabel)
	// Issue Comments
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/comments", authed(wsCtx(http.HandlerFunc(issues.ListComments))))
	r.authedMut("POST", "/api/v1/crews/{crewId}/issues/{identifier}/comments", roleCreate, issues.CreateComment)
	// Issue Relations
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/relations", authed(wsCtx(http.HandlerFunc(issues.ListRelations))))
	r.authedMut("POST", "/api/v1/crews/{crewId}/issues/{identifier}/relations", roleCreate, issues.CreateRelation)
	r.authedMut("DELETE", "/api/v1/relations/{relationId}", roleCreate, issues.DeleteRelation)
	// Projects
	projects := NewProjectHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/projects", authed(wsCtx(http.HandlerFunc(projects.List))))
	r.authedMut("POST", "/api/v1/projects", roleCreate, projects.Create)
	r.mux.Handle("GET /api/v1/projects/{projectId}", authed(wsCtx(http.HandlerFunc(projects.Get))))
	r.authedMut("PATCH", "/api/v1/projects/{projectId}", roleCreate, projects.Update)
	r.authedMut("DELETE", "/api/v1/projects/{projectId}", roleManage, projects.Delete)
	r.mux.Handle("GET /api/v1/projects/{projectId}/stats", authed(wsCtx(http.HandlerFunc(projects.Stats))))

	// Milestones
	milestones := NewMilestoneHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/projects/{projectId}/milestones", authed(wsCtx(http.HandlerFunc(milestones.List))))
	r.authedMut("POST", "/api/v1/projects/{projectId}/milestones", roleCreate, milestones.Create)
	r.authedMut("PATCH", "/api/v1/milestones/{milestoneId}", roleCreate, milestones.Update)
	r.authedMut("DELETE", "/api/v1/milestones/{milestoneId}", roleManage, milestones.Delete)
	// Notifications
	notifications := NewNotificationHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/notifications", authed(wsCtx(http.HandlerFunc(notifications.List))))
	r.mux.Handle("GET /api/v1/notifications/count", authed(wsCtx(http.HandlerFunc(notifications.Count))))
	r.authedMut("POST", "/api/v1/notifications/{notificationId}/read", roleSelf, notifications.MarkRead)
	r.authedMut("POST", "/api/v1/notifications/read-all", roleSelf, notifications.MarkAllRead)
	r.authedMut("DELETE", "/api/v1/notifications/{notificationId}", roleSelf, notifications.Delete)
	// Saved Views
	savedViews := NewSavedViewHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/saved-views", authed(wsCtx(http.HandlerFunc(savedViews.List))))
	r.authedMut("POST", "/api/v1/saved-views", roleCreate, savedViews.Create)
	r.authedMut("PATCH", "/api/v1/saved-views/{viewId}", roleCreate, savedViews.Update)
	r.authedMut("DELETE", "/api/v1/saved-views/{viewId}", roleSelf, savedViews.Delete)
	// Recurring Issues
	recurringIssues := NewRecurringIssueHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/recurring-issues", authed(wsCtx(http.HandlerFunc(recurringIssues.List))))
	r.authedMut("POST", "/api/v1/recurring-issues", roleCreate, recurringIssues.Create)
	r.authedMut("PATCH", "/api/v1/recurring-issues/{recurringId}", roleCreate, recurringIssues.Update)
	r.authedMut("DELETE", "/api/v1/recurring-issues/{recurringId}", roleManage, recurringIssues.Delete)
	// Triage Rules
	triage := NewTriageHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/triage-rules", authed(wsCtx(http.HandlerFunc(triage.ListRules))))
	r.authedMut("POST", "/api/v1/triage-rules", roleCreate, triage.CreateRule)
	r.authedMut("PATCH", "/api/v1/triage-rules/{ruleId}", roleCreate, triage.UpdateRule)
	r.authedMut("DELETE", "/api/v1/triage-rules/{ruleId}", roleManage, triage.DeleteRule)
	r.authedMut("POST", "/api/v1/triage/process", roleCreate, triage.Process)
	// Workflow Templates (custom issue status flows). SPEC-2: new in this PR.
	wt := NewWorkflowTemplateHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/workflow-templates", authed(wsCtx(http.HandlerFunc(wt.List))))
	r.authedMut("POST", "/api/v1/workflow-templates", roleCreate, wt.Create)
	r.mux.Handle("GET /api/v1/workflow-templates/{id}", authed(wsCtx(http.HandlerFunc(wt.Get))))
	r.authedMut("PATCH", "/api/v1/workflow-templates/{id}", roleCreate, wt.Update)
	r.authedMut("DELETE", "/api/v1/workflow-templates/{id}", roleCreate, wt.Delete)
	// Feature Flags (instance-default + per-workspace override). SPEC-2: new in this PR.
	ff := NewFeatureFlagHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/feature-flags", authed(wsCtx(http.HandlerFunc(ff.List))))
	r.authedMut("POST", "/api/v1/feature-flags", roleManage, ff.Create)
	r.authedMut("PATCH", "/api/v1/feature-flags/{key}", roleManage, ff.Update)
	r.authedMut("DELETE", "/api/v1/feature-flags/{key}", roleManage, ff.Delete)
	r.authedMut("PUT", "/api/v1/feature-flags/{key}/override", roleManage, ff.UpsertOverride)
	r.authedMut("DELETE", "/api/v1/feature-flags/{key}/override", roleManage, ff.DeleteOverride)
	// Instance Settings (admin-only key/value config). SPEC-2: new in this PR.
	inst := NewInstanceSettingsHandler(r.db, r.hub, r.logger)
	r.mux.Handle("GET /api/v1/instance/settings", authed(wsCtx(http.HandlerFunc(inst.List))))
	r.mux.Handle("GET /api/v1/instance/settings/{key}", authed(wsCtx(http.HandlerFunc(inst.Get))))
	r.authedMut("PUT", "/api/v1/instance/settings/{key}", roleManage, inst.Put)
	r.authedMut("DELETE", "/api/v1/instance/settings/{key}", roleManage, inst.Delete)
	// Issue Bulk Operations
	r.authedMut("PATCH", "/api/v1/issues/bulk", roleCreate, issues.BulkUpdate)
	// Issue Sub-issues
	r.mux.Handle("GET /api/v1/crews/{crewId}/issues/{identifier}/subtasks", authed(wsCtx(http.HandlerFunc(issues.ListSubIssues))))

	// Runs (require workspace context)
	runs := NewRunHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/runs", authed(wsCtx(http.HandlerFunc(runs.List))))
	// Fleet operations aggregate. Registered as a literal path so Go's mux
	// prefers it over the {id} wildcard below (literal wins over pattern).
	r.mux.Handle("GET /api/v1/runs/insights", authed(wsCtx(http.HandlerFunc(runs.Insights))))
	r.mux.Handle("GET /api/v1/runs/{id}", authed(wsCtx(http.HandlerFunc(runs.Get))))

	// Crew Journal: workspace-wide event stream. Reads only — writes are
	// internal via journal.Writer emits from handlers across the codebase.
	jh := NewJournalHandler(r.db, r.logger, r.Journal())
	r.mux.Handle("GET /api/v1/journal", authed(wsCtx(http.HandlerFunc(jh.List))))
	r.mux.Handle("GET /api/v1/journal/stream", authed(wsCtx(http.HandlerFunc(jh.Stream))))
	r.mux.Handle("GET /api/v1/journal/count", authed(wsCtx(http.HandlerFunc(jh.Count))))
	// Cost rollup (#1404) — literal path, must be registered before the
	// {id} wildcard below (same "literal wins over pattern" ordering
	// convention as runs.Insights above).
	r.mux.Handle("GET /api/v1/journal/spend", authed(wsCtx(http.HandlerFunc(jh.Spend))))
	// Single-entry GET — needed by deep-links from the timeline (e.g. a
	// shared URL pointing at one keeper.decision row) and by the CLI's
	// `journal get <id>`. Workspace-scoped; cross-tenant IDs return 404
	// with the same shape as "not found".
	r.mux.Handle("GET /api/v1/journal/{id}", authed(wsCtx(http.HandlerFunc(jh.Get))))
	r.authedMut("POST", "/api/v1/journal/{id}/priority", roleManage, jh.SetPriority)
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
	r.authedMut("POST", "/api/v1/missions/{missionId}/checkpoints", roleCreate, ch.Create)
	r.mux.Handle("GET /api/v1/checkpoints/{id}", authed(wsCtx(http.HandlerFunc(ch.Get))))
	r.authedMut("POST", "/api/v1/checkpoints/{id}/restore", roleCreate, ch.Restore)
	r.authedMut("POST", "/api/v1/checkpoints/{id}/fork", roleCreate, ch.Fork)
	r.authedMut("DELETE", "/api/v1/checkpoints/{id}", roleCreate, ch.Delete)

	// Notification channels: outbound e-mail + signed-webhook delivery on
	// run completion/failure (#850). Writes are MANAGER+. The mailer is
	// resolved from env (RESEND_*) here, mirroring the recovery handler;
	// an email channel is rejected at create when no transport is wired.
	nch := NewNotifyChannelHandler(r.db, mailer.NewFromEnv(), r.logger)
	r.mux.Handle("GET /api/v1/notification-channels", authed(wsCtx(http.HandlerFunc(nch.List))))
	r.authedMut("POST", "/api/v1/notification-channels", roleCreate, nch.Create)
	r.authedMut("DELETE", "/api/v1/notification-channels/{id}", roleCreate, nch.Delete)
	r.authedMut("POST", "/api/v1/notification-channels/{id}/test", roleCreate, nch.Test)

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
	// Hub is what lets an ephemeral-hire decision (issue #1209) push the
	// agent status flip to open dashboards without a poll.
	ah.SetHub(r.hub)
	r.mux.Handle("GET /api/v1/approvals", authed(wsCtx(http.HandlerFunc(ah.List))))
	r.mux.Handle("GET /api/v1/approvals/{id}", authed(wsCtx(http.HandlerFunc(ah.Get))))
	r.authedMut("POST", "/api/v1/approvals/{id}/decide", roleManage, ah.Decide)
	r.authedMut("POST", "/api/v1/approvals/{id}/cancel", roleManage, ah.Cancel)
	r.authedMut("POST", "/api/v1/approvals/reset-auto-tuning", roleManage, ah.ResetAutoTuning)

	// Unified Inbox — the canonical "stuff that needs the human"
	// surface. Backed by inbox_items (migration v85). Source-of-
	// truth handlers (waitpoints, escalations, run failures) write
	// through to this table so the bell + /inbox page render from
	// one query instead of fanning out to four.
	ih := NewInboxHandler(r.db, r.logger, r.hub)
	r.mux.Handle("GET /api/v1/inbox", authed(wsCtx(http.HandlerFunc(ih.List))))
	r.mux.Handle("GET /api/v1/inbox/count", authed(wsCtx(http.HandlerFunc(ih.UnreadCount))))
	r.mux.Handle("GET /api/v1/inbox/{id}", authed(wsCtx(http.HandlerFunc(ih.Get))))
	r.authedMut("PATCH", "/api/v1/inbox/{id}", roleSelf, ih.PatchState)
	// Bulk state transition — the tree-grouped UI's "resolve all under
	// this routine / crew" action. POST so the body can carry the id list.
	r.authedMut("POST", "/api/v1/inbox/bulk", roleSelf, ih.BulkPatchState)
	// Hard purge (admin-only) — teardown primitive for seed --nuke and
	// operator spam-cleanup. Optional ?kind= scopes the wipe.
	r.authedMut("DELETE", "/api/v1/inbox", roleManage, ih.Purge)

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
	r.authedSelfMut("PUT", "/api/v1/me/preferences/{key}", uph.Set)
	r.authedSelfMut("DELETE", "/api/v1/me/preferences/{key}", uph.Delete)

	// Message reactions: per-(chat, message, emoji, user) emoji react with
	// idempotent INSERT OR IGNORE. Migration v57 created the underlying
	// table; endpoints are scoped via chats.workspace_id so cross-tenant
	// reads/writes return 404.
	mrh := NewMessageReactionsHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/chats/{chatId}/messages/{messageId}/reactions",
		authed(http.HandlerFunc(mrh.List)))
	r.authedSelfMut("POST", "/api/v1/chats/{chatId}/messages/{messageId}/reactions", mrh.Add)
	r.authedSelfMut("DELETE", "/api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}", mrh.Remove)

	// Chat participants: who is in a multi-user group chat. Adding the first
	// extra participant flips chats.visibility to 'group' (agent responds only
	// on @mention). Scoped via chats.workspace_id like reactions.
	cph := NewChatParticipantsHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/chats/{chatId}/participants",
		authed(http.HandlerFunc(cph.List)))
	r.authedSelfMut("POST", "/api/v1/chats/{chatId}/participants", cph.Add)
	r.authedSelfMut("DELETE", "/api/v1/chats/{chatId}/participants/{userId}", cph.Remove)

	// Mid-turn steering: POST a steering message into a chat. The bridge
	// guards against racing a second run into a live turn — today the
	// message is QUEUED for the next turn (live injection is a deferred
	// follow-up). Scoped via chats.workspace_id like reactions, so
	// cross-tenant returns 404. The Steerer (chatbridge.Bridge) is wired
	// post-construction via SetSteerer; until then the route returns 503.
	r.steerHandler = NewSteerHandler(r.db, r.steerer, r.logger)
	r.authedSelfMut("POST", "/api/v1/chats/{chatId}/steer", r.steerHandler.Steer)

	// Typed feedback signal: thumbs / edit / regenerate bound to trace_id
	// for the ADLC phase-7 continuous-learning loop. Sits beside reactions
	// rather than replacing it — reactions are open-vocabulary social
	// signal, feedback is structured eval signal. Migration v96 owns
	// the table.
	mfh := NewMessageFeedbackHandler(r.db, r.logger)
	r.authedSelfMut("POST", "/api/v1/feedback", mfh.Create)
	r.mux.Handle("GET /api/v1/feedback", authed(http.HandlerFunc(mfh.List)))
	r.authedSelfMut("DELETE", "/api/v1/feedback", mfh.Delete)

	// Hooks registry: lifecycle intercepts. List is available to every
	// workspace member for auditability; enable/disable is OWNER/ADMIN
	// only because flipping a hook can invoke shell commands.
	hh := NewHooksHandler(r.db, r.logger)
	hh.SetJournal(r.Journal())
	r.mux.Handle("GET /api/v1/hooks", authed(wsCtx(http.HandlerFunc(hh.List))))
	r.authedMut("POST", "/api/v1/hooks/{id}/enable", roleManage, hh.Enable)
	r.authedMut("POST", "/api/v1/hooks/{id}/disable", roleManage, hh.Disable)

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
	r.authedMut("POST", "/api/v1/consolidate/run", roleManage, conH.Run)

	// PostRunTrigger — sleep-time consolidator hook (PRD §8.1). Built
	// only when a consolidator + memory root are wired; otherwise we
	// leave the trigger nil and InternalHandler.UpdateRun no-ops on
	// the call site. The trigger does its own per-(workspace, crew)
	// debouncing (default 30 min) so a chatty crew with many short
	// runs doesn't dogpile the LLM extractor.
	var postRunTrigger postRunTriggerHook
	if r.consolidator != nil && r.consolidateMemoryRoot != "" {
		postRunTrigger = consolidate.NewPostRunTrigger(r.consolidator, consolidate.PostRunTriggerOptions{
			CrewMemoryRoot: r.consolidateMemoryRoot,
			BlobRoot:       r.memoryVersionsBlobRoot,
			Logger:         r.logger,
		})
	}

	// HITL proposal decision surface. When the consolidator runs in
	// ProposalMode (CREWSHIP_CONSOLIDATE_HITL=1), each extracted rule
	// set lands in .proposed/proposal-*.md + a memory_proposals row +
	// an inbox_items entry — these three endpoints are the human-
	// decision side of that flow. Explain is read-only (MEMBER+);
	// approve/reject are gated to OWNER/ADMIN by the handler itself.
	propH := NewProposedHandler(r.db, r.logger)
	propH.SetJournal(r.Journal())
	propH.SetBlobRoot(r.memoryVersionsBlobRoot)
	r.authedMut("POST", "/api/v1/consolidate/proposed/{id}/approve", roleManage, propH.Approve)
	r.authedMut("POST", "/api/v1/consolidate/proposed/{id}/reject", roleManage, propH.Reject)
	r.mux.Handle("GET /api/v1/consolidate/proposed/{id}/explain", authed(wsCtx(http.HandlerFunc(propH.Explain))))
	// Diff: preview the byte-level change an approve would land in
	// the canonical learned-*.md file. Read-only, MEMBER+ — same
	// auth surface as Explain. Pairs with the existing approve/
	// reject flow so the HITL UI can show a diff before the
	// operator commits.
	r.mux.Handle("GET /api/v1/consolidate/proposed/{id}/diff", authed(wsCtx(http.HandlerFunc(propH.Diff))))

	// memory→Skills bridge HITL surface (PR #4 step 7). When the
	// consolidator auto-promotes a stable learned rule into
	// .proposed/skill-{slug}.md, these three endpoints let an
	// operator list, approve (import via the canonical skills
	// importer), or reject (delete) the staged SKILL.md. Handler is
	// stateless — disk is the source of truth; audit is via journal
	// entries. OWNER/ADMIN/MANAGER gating happens inside the handler
	// to match the canonical skill-import permission.
	skillPropH := NewSkillProposedHandler(r.db, r.logger)
	skillPropH.SetJournal(r.Journal())
	skillPropH.SetCrewMemoryRoot(r.consolidateMemoryRoot)
	// Stash for registerInternalRoutes (runs after this) so the internal
	// agent-authoring route can reuse this instance (shared db/journal/root).
	r.skillPropHandler = skillPropH
	r.mux.Handle("GET /api/v1/skills/proposed", authed(wsCtx(http.HandlerFunc(skillPropH.List))))
	r.authedMut("POST", "/api/v1/skills/proposed/approve", roleManage, skillPropH.Approve)
	r.authedMut("POST", "/api/v1/skills/proposed/reject", roleManage, skillPropH.Reject)

	// Memory versions audit surface — the HTTP mirror of `crewship
	// memory log/show/restore`. List + show are MEMBER+ (read-only
	// audit visibility); restore is OWNER/ADMIN-gated inside the
	// handler. Workspace anchoring lives in the handler too — query
	// strings never carry workspace_id, so a cross-workspace probe
	// can't smuggle a foreign id through.
	mvH := NewMemoryVersionsHandler(r.db, r.logger)
	mvH.SetBlobRoot(r.memoryVersionsBlobRoot)
	r.mux.Handle("GET /api/v1/memory/versions", authed(wsCtx(http.HandlerFunc(mvH.List))))
	r.mux.Handle("GET /api/v1/memory/versions/{sha}", authed(wsCtx(http.HandlerFunc(mvH.Show))))
	r.authedMut("POST", "/api/v1/memory/versions/{sha}/restore", roleManage, mvH.Restore)

	// Host-side hybrid search — combines workspace FTS + episodic
	// vec+BM25 via RRF (memory.HybridSearch primitive). MEMBER+ at
	// the handler level; workspace-anchored so cross-workspace
	// probes can't smuggle foreign ids.
	hsH := NewMemoryHybridSearchHandler(r.db, r.logger)
	hsH.SetEmbedder(r.hybridSearchEmbedder)
	hsH.SetWorkspaceProvider(r.hybridSearchProvider)
	hsH.SetJournal(r.Journal())
	// Stash for registerInternalRoutes (runs after this): the sidecar's
	// hybrid forward hits an internal-token route (#1348) that must share
	// this instance (same embedder/provider/journal wiring).
	r.hybridSearchHandler = hsH
	r.authedMut("POST", "/api/v1/memory/search/hybrid", roleSelf, hsH.Search)

	// Quartermaster eval: mission replay + regression + list. Both
	// mutating calls run in a goroutine and return 202 with a run_id
	// the caller can later poll for in the list endpoint.
	evH := NewEvalHandler(r.db, r.logger)
	evH.SetJournal(r.Journal())
	r.authedMut("POST", "/api/v1/eval/replay", roleManage, evH.Replay)
	r.authedMut("POST", "/api/v1/eval/regression", roleManage, evH.Regression)
	r.mux.Handle("GET /api/v1/eval/runs", authed(wsCtx(http.HandlerFunc(evH.ListRuns))))
	r.mux.Handle("GET /api/v1/eval/runs/{id}", authed(wsCtx(http.HandlerFunc(evH.GetRun))))

	// MCP tool call audit (require workspace context)
	mcpAudit := NewMCPAuditHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/mcp-tool-calls", authed(wsCtx(http.HandlerFunc(mcpAudit.List))))

	// MCP Registry (public browsing, auth required; manual sync requires workspace member)
	mcpRegistry := NewMCPRegistryHandler(r.db, r.logger)
	r.mux.Handle("GET /api/v1/mcp-registry", authed(http.HandlerFunc(mcpRegistry.List)))
	r.mux.Handle("GET /api/v1/mcp-registry/search", authed(http.HandlerFunc(mcpRegistry.Search)))
	r.authedMut("POST", "/api/v1/mcp-registry/sync", roleManage, mcpRegistry.Sync)

	// OAuth flow (auth required for initiate, callback is unauthenticated — uses state token)
	oauth := NewOAuthHandler(r.db, r.logger)
	if r.hub != nil {
		oauth.SetHub(r.hub)
	}
	r.mux.Handle("GET /api/v1/oauth/providers", authed(http.HandlerFunc(oauth.ListProviders)))
	// initiate/exchange/loopback run the layered role-OR-capability gate
	// inside the handler (#1034): MANAGER+ or credential.create, aligned
	// with POST /credentials since the flow lands the same OAUTH2 row.
	r.authedMut("POST", "/api/v1/oauth/initiate", roleInline, oauth.Initiate)
	r.mux.HandleFunc("GET /api/v1/oauth/callback", oauth.Callback) // No auth — uses state token
	r.authedMut("POST", "/api/v1/oauth/exchange", roleInline, oauth.Exchange)
	r.authedMut("POST", "/api/v1/oauth/loopback", roleInline, oauth.Loopback)
	r.authedSelfMut("POST", "/api/v1/oauth/discover", oauth.Discover)
	r.authedMut("POST", "/api/v1/oauth/auto-connect", roleManage, oauth.AutoConnect)

	// Crewshipd proxy + agent runtime routes (require IPC socket)
	socketPath := r.socketPath
	if socketPath == "" {
		socketPath = config.DefaultSocketPath()
	}
	proxy := NewProxyHandler(r.db, r.logger, socketPath)
	r.mux.Handle("GET /api/v1/crewshipd", authed(wsCtx(http.HandlerFunc(proxy.CrewshipdHealth))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/debug", authed(wsCtx(http.HandlerFunc(proxy.AgentDebug))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/files", authed(wsCtx(http.HandlerFunc(proxy.AgentFiles))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/files/download", authed(wsCtx(http.HandlerFunc(proxy.AgentFileDownload))))
	r.authedMut("PUT", "/api/v1/agents/{agentId}/files/save", roleCreate, proxy.AgentFileSave)
	// Multipart upload tied to a (agent, chat) pair. Lands at
	// /output/<slug>/attachments/<chatId>/<filename> on the agent side.
	r.authedMut("POST", "/api/v1/agents/{agentId}/chats/{chatId}/attachments", roleCreate, proxy.AgentChatAttachment)
	r.mux.Handle("GET /api/v1/crews/{crewId}/files", authed(wsCtx(http.HandlerFunc(proxy.CrewFiles))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/files/download", authed(wsCtx(http.HandlerFunc(proxy.CrewFileDownload))))
	r.authedMut("PUT", "/api/v1/crews/{crewId}/files/save", roleCreate, proxy.CrewFileSave)
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/files/delete", roleCreate, proxy.CrewFileDelete)
	r.mux.Handle("GET /api/v1/agents/{agentId}/container-files", authed(wsCtx(http.HandlerFunc(proxy.AgentContainerFiles))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/git-log", authed(wsCtx(http.HandlerFunc(proxy.AgentGitLog))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/git-diff", authed(wsCtx(http.HandlerFunc(proxy.CrewGitDiff))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/changes", authed(wsCtx(http.HandlerFunc(proxy.RunGitDiff))))
	// Run→files: the artefacts a run produced, inferred from the crew's
	// file mtimes within the run window (#839).
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/files", authed(wsCtx(http.HandlerFunc(proxy.RunFiles))))
	r.mux.Handle("GET /api/v1/agents/{agentId}/logs", authed(wsCtx(http.HandlerFunc(proxy.AgentLogs))))
	r.authedMut("POST", "/api/v1/agents/{agentId}/stop", roleCreate, proxy.AgentStop)
	// wsCtx is required because ChatMessages runs canRole(RoleFromContext)
	// for the workspace's read-tier gate (audit #495 follow-up). Without
	// wsCtx the role context value is empty, the gate fail-closes, and
	// every authenticated user — including OWNER — gets 403. Caught live
	// on 2026-05-22 audit (issue #539); sibling POST route at line 412
	// already wraps the full chain.
	r.mux.Handle("GET /api/v1/chats/{chatId}/messages", authed(wsCtx(http.HandlerFunc(proxy.ChatMessages))))

	// Assignment routes (internal auth, called by sidecar on behalf of lead agents).
	// AssignmentHandler is constructed here so the public list endpoint
	// below shares the same instance; the internal-side POST/GET are
	// registered in router_internal.go.
	assign := NewAssignmentHandler(r.db, r.orch, r.hub, r.internalToken, r.logger)
	assign.SetJournal(r.Journal())
	// #810: route mission / sidecar-assign dispatch through the one
	// request-builder so sub-agents get the assembled prompt + MCP + skills
	// + crew-policy ApprovalMode instead of raw system_prompt_legacy. Shares
	// the same in-process internal-resolve URL the webhook path uses.
	if dispatchBaseURL := r.internalLoopbackURL; dispatchBaseURL != "" || r.internalBaseURL != "" {
		if dispatchBaseURL == "" {
			dispatchBaseURL = r.internalBaseURL
		}
		assign.SetResolver(chatbridge.NewIPCResolver(dispatchBaseURL, r.internalToken, r.logger))
	}
	// Stash on the Router so the server boot path can start the
	// stuck-QUEUED sweeper on this same instance (Assignments()).
	r.assignmentHandler = assign
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
	// #810: same builder routing for the peer-query path.
	if dispatchBaseURL := r.internalLoopbackURL; dispatchBaseURL != "" || r.internalBaseURL != "" {
		if dispatchBaseURL == "" {
			dispatchBaseURL = r.internalBaseURL
		}
		queries.SetResolver(chatbridge.NewIPCResolver(dispatchBaseURL, r.internalToken, r.logger))
	}
	// Provisioning gate for the peer-query path: registerCrewsRoutes (which sets
	// r.provisioning) runs before this, so a cold target crew builds its image
	// before a peer query runs instead of booting the bare base.
	if r.provisioning != nil {
		queries.SetProvisioner(r.provisioning)
	}

	// Crew peer conversations, standup, and escalations (public, authenticated)
	r.mux.Handle("GET /api/v1/crews/{crewId}/peer-conversations", authed(wsCtx(http.HandlerFunc(queries.ListPeerConversations))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/standup", authed(wsCtx(http.HandlerFunc(queries.Standup))))
	r.mux.Handle("GET /api/v1/crews/{crewId}/escalations", authed(wsCtx(http.HandlerFunc(queries.ListEscalations))))
	r.authedMut("PATCH", "/api/v1/escalations/{escalationId}/resolve", roleCreate, queries.ResolveEscalation)
	// Hard purge of a crew's escalations (admin-only) — teardown primitive for
	// seed --nuke; escalations have no workspace FK, so a crew delete orphans them.
	r.authedMut("DELETE", "/api/v1/crews/{crewId}/escalations", roleManage, queries.PurgeEscalations)

	// Workspace-wide escalation count (public, authenticated)
	r.mux.Handle("GET /api/v1/escalations/pending-count", authed(wsCtx(http.HandlerFunc(queries.PendingEscalationCount))))

	// Cross-session conversation search (public, authenticated). Backed by
	// the conversation_messages FTS5 mirror (v111). The handler verifies
	// the requested agent belongs to the caller's workspace before the
	// agent-scoped BM25 query runs.
	convSearchHandler := NewConversationHandler(r.db, r.convSearcher)
	r.authedMut("POST", "/api/v1/conversations/search", roleSelf, convSearchHandler.Search)

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
			inspectResult, err := dc.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if err != nil {
				return "", err
			}
			inspect := inspectResult.Container
			if inspect.NetworkSettings == nil {
				return "", errPortExposeNoNetwork
			}
			ns, ok := inspect.NetworkSettings.Networks[network]
			if !ok || ns == nil || !ns.IPAddress.IsValid() {
				return "", errPortExposeNoNetwork
			}
			return ns.IPAddress.String(), nil
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
	r.authedMut("POST", "/api/v1/crews/{crewId}/port-expose/{id}/revoke", roleCreate, portExposeH.Revoke)

	// Webhooks (public, HMAC-secret protected)
	if r.orch != nil && r.keeperContainer != nil && r.logWriter != nil && r.internalToken != "" {
		// Pick the URL that's actually reachable from inside *this* process.
		// internalLoopbackURL (127.0.0.1:<port>) is the right pick when
		// the daemon is calling its own internal API: container-facing
		// internalBaseURL like host.docker.internal:<port> resolves via
		// /etc/hosts on the host and can land at the wrong machine on
		// multi-host lab nets — that's exactly how issue #535 manifested
		// on dev1 (host.docker.internal resolved to a different VM).
		// Fall back to internalBaseURL only when the loopback URL wasn't
		// plumbed in. No more silent `http://localhost:8080` fallback:
		// the magic value masked multi-instance setups for a long time
		// (#538), so we fail fast at boot instead.
		baseURL := r.internalLoopbackURL
		if baseURL == "" {
			baseURL = r.internalBaseURL
		}
		if baseURL == "" {
			r.logger.Error("cannot wire agent-webhook route: neither internalLoopbackURL nor internalBaseURL is set (#538)")
		} else {
			resolver := chatbridge.NewIPCResolver(baseURL, r.internalToken, r.logger)
			wh := NewWebhookHandler(r.db, r.logger, resolver, r.orch, r.hub, r.keeperContainer, r.logWriter)
			r.mux.Handle("POST /api/v1/webhooks/{crewId}/{agentId}/trigger", http.HandlerFunc(wh.ServeHTTP))
		}
	}

	return orchestrationHandlers{
		assign:         assign,
		queries:        queries,
		portExposeH:    portExposeH,
		postRunTrigger: postRunTrigger,
	}
}
