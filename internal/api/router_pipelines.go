package api

// Public pipeline routes: CRUD, versioning, runs, schedules,
// webhooks (CRUD + public dispatch). The internal /pipelines/save
// route lives in router_internal.go because it shares the
// X-Internal-Token auth chain with the other sidecar IPC endpoints.

import "net/http"

// registerPipelineRoutes wires the public pipeline / schedules /
// webhooks surface. Returns the PipelineHandler so the orchestrator
// can (a) stash it on the Router for post-construction AgentRunner
// wiring and (b) hand `InternalSave` to the internal-routes registrar.
func (r *Router) registerPipelineRoutes() *PipelineHandler {
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// Pipelines — declarative DSL workflows persisted per-workspace and
	// reusable across crews. Runner is wired post-construction by the
	// orchestrator boot path; an unwired runner returns 503 from /run
	// and /test_run so the rest of the surface (List/Get/Delete/DryRun)
	// stays usable for read-only inspection during boot.
	pipes := NewPipelineHandler(r.db, r.logger, nil, nil)
	r.PipelinesHandler = pipes // expose for orchestrator wiring
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines", authed(wsCtx(http.HandlerFunc(pipes.List))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}", authed(wsCtx(http.HandlerFunc(pipes.Get))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run", authed(wsCtx(http.HandlerFunc(pipes.Run))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch", authed(wsCtx(http.HandlerFunc(pipes.RunBatch))))
	// Per-step prompt/model override layer (v121) — tweak a step without
	// bumping the routine version.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/overrides", authed(wsCtx(http.HandlerFunc(pipes.ListStepOverrides))))
	r.mux.Handle("PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override", authed(wsCtx(http.HandlerFunc(pipes.SetStepOverride))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override", authed(wsCtx(http.HandlerFunc(pipes.DeleteStepOverride))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run", authed(wsCtx(http.HandlerFunc(pipes.DryRun))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/test_run", authed(wsCtx(http.HandlerFunc(pipes.TestRun))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}", authed(wsCtx(http.HandlerFunc(pipes.Delete))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/runs", authed(wsCtx(http.HandlerFunc(pipes.ListRuns))))
	// run-records hits the v83 pipeline_runs table directly — column-typed
	// reads beat the LIKE+json_extract scan ListRuns does over journal_entries.
	// Returns 503 when runStore is not wired so legacy clients can fall
	// back gracefully to /runs.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run-records", authed(wsCtx(http.HandlerFunc(pipes.ListRunRecords))))
	// Versioning — every save creates an immutable history row;
	// rollback flips head to a prior version (history preserved).
	// User-facing save (UI "New routine" flow). MANAGER+ role
	// required. Distinct from /internal/pipelines/save which the
	// sidecar uses with X-Internal-Token; this route uses normal
	// JWT auth and records authorship as the calling user.
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/save", authed(wsCtx(http.HandlerFunc(pipes.Save))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/versions", authed(wsCtx(http.HandlerFunc(pipes.ListVersions))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/versions/{n}", authed(wsCtx(http.HandlerFunc(pipes.GetVersion))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/rollback", authed(wsCtx(http.HandlerFunc(pipes.Rollback))))
	// Marketplace prep: portable JSON bundles for cross-workspace
	// transfer. Export is read-only; import requires author_crew_id
	// in the body since bundles are deliberately
	// installation-independent.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/export", authed(wsCtx(http.HandlerFunc(pipes.ExportPipeline))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/import", authed(wsCtx(http.HandlerFunc(pipes.ImportPipeline))))
	// Waitpoints — StepWait approval persistence + UI inbox surface.
	// Pending waitpoints flow into the same Inbox as Keeper approvals.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/waitpoints", authed(wsCtx(http.HandlerFunc(pipes.ListPendingWaitpoints))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/waitpoints/{token}/approve", authed(wsCtx(http.HandlerFunc(pipes.ApproveWaitpoint))))
	// Pipeline schedules — cron triggers for saved pipelines (the
	// Routines integration). CRUD-only; the scheduler runs in-process
	// in cmd_start and reads the table directly.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-schedules", authed(wsCtx(http.HandlerFunc(pipes.ListSchedules))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipeline-schedules", authed(wsCtx(http.HandlerFunc(pipes.CreateSchedule))))
	r.mux.Handle("PATCH /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}", authed(wsCtx(http.HandlerFunc(pipes.UpdateSchedule))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}", authed(wsCtx(http.HandlerFunc(pipes.DeleteSchedule))))
	// Force-fire a schedule out of cycle (CLI: `routine schedules now`).
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/run", authed(wsCtx(http.HandlerFunc(pipes.RunSchedule))))
	// Run control — cancel + active list. The cancel API is the
	// other half of concurrency control: a stuck run holds a slot
	// until either it finishes or the operator pre-empts it.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/runs/active", authed(wsCtx(http.HandlerFunc(pipes.ListActiveRuns))))
	// Single-run + workspace-list lookups under /pipeline-runs/ (top-
	// level resource) instead of /pipelines/runs/ because the latter
	// collides with /pipelines/{slug}/runs in net/http's pattern-
	// matcher: both resolve "/pipelines/runs/runs" without a tie-
	// breaker. Workspace list also stays out of /pipelines/{slug}/
	// because it spans every pipeline.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs", authed(wsCtx(http.HandlerFunc(pipes.ListWorkspaceRuns))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}", authed(wsCtx(http.HandlerFunc(pipes.GetRun))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/cancel", authed(wsCtx(http.HandlerFunc(pipes.CancelRun))))
	// Observability (trigger.dev-informed): replay a failed run with its
	// original inputs, bulk-replay a fingerprint group, and list failures
	// bucketed by fingerprint. errors/bulk_replay are registered before
	// the {runId} replay so the literal segments win net/http matching.
	// Deferred dispatch (delay/ttl/debounce/priority) — list + cancel
	// parked triggers. Registered before {slug} routes so the literal
	// "pending" segment wins net/http matching.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/pending", authed(wsCtx(http.HandlerFunc(pipes.ListPendingRuns))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel", authed(wsCtx(http.HandlerFunc(pipes.CancelPendingRun))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/runs/errors", authed(wsCtx(http.HandlerFunc(pipes.ListErrorGroups))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay", authed(wsCtx(http.HandlerFunc(pipes.BulkReplayRuns))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay", authed(wsCtx(http.HandlerFunc(pipes.ReplayRun))))
	// Pipeline webhooks — event-driven trigger surface alongside
	// cron schedules. CRUD requires auth; the public dispatch
	// endpoint authenticates via the token + optional HMAC instead.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-webhooks", authed(wsCtx(http.HandlerFunc(pipes.ListWebhooks))))
	r.mux.Handle("POST /api/v1/workspaces/{workspaceId}/pipeline-webhooks", authed(wsCtx(http.HandlerFunc(pipes.CreateWebhook))))
	r.mux.Handle("DELETE /api/v1/workspaces/{workspaceId}/pipeline-webhooks/{webhookId}", authed(wsCtx(http.HandlerFunc(pipes.DeleteWebhook))))
	// Public dispatch — no `authed` wrapper. The token in the path
	// is the auth surface; signing_secret + HMAC layered on top.
	r.mux.HandleFunc("POST /api/v1/webhooks/{token}", pipes.FireWebhook)
	// Public waitpoint completion — an external system completes a wait
	// via callback URL with no workspace JWT (the high-entropy token is
	// the auth, same model as webhook dispatch). Surfaced as callback_url
	// on the pending-waitpoints list.
	r.mux.HandleFunc("POST /api/v1/waitpoint-tokens/{token}", pipes.CompleteWaitpointToken)
	// Internal /api/v1/internal/pipelines/save route is registered
	// alongside the other /internal endpoints in router_internal.go.

	return pipes
}
