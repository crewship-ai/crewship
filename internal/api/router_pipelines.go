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
	// so the rest of the surface (List/Get/Delete/DryRun) stays usable
	// for read-only inspection during boot. There is no public test_run
	// route — the only draft validation gate is the internal save gate
	// (/internal/pipelines/test_run, dry-run); a real run is just /run.
	pipes := NewPipelineHandler(r.db, r.logger, nil, nil)
	r.PipelinesHandler = pipes // expose for orchestrator wiring
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines", authed(wsCtx(http.HandlerFunc(pipes.List))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}", authed(wsCtx(http.HandlerFunc(pipes.Get))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/run", roleCreate, pipes.Run)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch", roleCreate, pipes.RunBatch)
	// Per-step prompt/model override layer (v121) — tweak a step without
	// bumping the routine version.
	r.authedMut("PUT", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags", roleCreate, pipes.AddPipelineTags)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags/{tag}", roleCreate, pipes.RemovePipelineTag)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/overrides", authed(wsCtx(http.HandlerFunc(pipes.ListStepOverrides))))
	r.authedMut("PUT", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override", roleCreate, pipes.SetStepOverride)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override", roleCreate, pipes.DeleteStepOverride)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run", roleCreate, pipes.DryRun)
	// Single-step debug: execute ONE agent_run step against a fixture, no DAG,
	// no persisted run record. The "unit test for a step" (see StepRun).
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/step_run", roleCreate, pipes.StepRun)
	// Public draft-validation gate behind the UI "Test run" button: dry-run
	// validates an UNSAVED definition and mints the save_token Save verifies.
	// Distinct from /internal/pipelines/test_run (sidecar, X-Internal-Token) —
	// this one is JWT-authed so a browser/CLI can reach it. Literal "test_run"
	// beats the {slug} catch-all in net/http matching, so no ordering hazard.
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/test_run", roleCreate, pipes.TestRun)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}", roleManage, pipes.Delete)
	// Routine governance (maker-checker + admin airbag). approve/reject are
	// MANAGER+ (canRole "create"); disable/enable are OWNER/ADMIN
	// (canRole "manage"). Literal sub-segments beat the {slug} catch-all in
	// net/http's matcher, so no ordering hazard.
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/approve", roleCreate, pipes.Approve)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/reject", roleCreate, pipes.Reject)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/disable", roleManage, pipes.Disable)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/enable", roleManage, pipes.Enable)
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
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/save", roleCreate, pipes.Save)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/versions", authed(wsCtx(http.HandlerFunc(pipes.ListVersions))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/versions/{n}", authed(wsCtx(http.HandlerFunc(pipes.GetVersion))))
	// #1422 item 5: native version diff (`?from=N&to=M`) — unified diff of
	// two versions' definitions, no external round-trip needed.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/diff", authed(wsCtx(http.HandlerFunc(pipes.DiffVersions))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/rollback", roleManage, pipes.Rollback)
	// Marketplace prep: portable JSON bundles for cross-workspace
	// transfer. Export is read-only; import requires author_crew_id
	// in the body since bundles are deliberately
	// installation-independent.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/export", authed(wsCtx(http.HandlerFunc(pipes.ExportPipeline))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/import", roleCreate, pipes.ImportPipeline)
	// Waitpoints — StepWait approval persistence + UI inbox surface.
	// Pending waitpoints flow into the same Inbox as Keeper approvals.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/waitpoints", authed(wsCtx(http.HandlerFunc(pipes.ListPendingWaitpoints))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/waitpoints/{token}/approve", roleCreate, pipes.ApproveWaitpoint)
	// Pipeline schedules — cron triggers for saved pipelines (the
	// Routines integration). CRUD-only; the scheduler runs in-process
	// in cmd_start and reads the table directly.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-schedules", authed(wsCtx(http.HandlerFunc(pipes.ListSchedules))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipeline-schedules", roleInline, pipes.CreateSchedule)
	r.authedMut("PATCH", "/api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}", roleManage, pipes.UpdateSchedule)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}", roleManage, pipes.DeleteSchedule)
	// Force-fire a schedule out of cycle (CLI: `routine schedules now`).
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/run", roleCreate, pipes.RunSchedule)
	// Run control — cancel + active list. The cancel API is the
	// other half of concurrency control: a stuck run holds a slot
	// until either it finishes or the operator pre-empts it.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/runs/active", authed(wsCtx(http.HandlerFunc(pipes.ListActiveRuns))))
	// Per-routine monthly budget meter (#1422 item 3): GET reads
	// budget-vs-actual for one routine, PATCH sets/clears the cap
	// (manage-tier — same as pausing/disabling a routine), and the
	// workspace roll-up lists every routine with a budget set or spend
	// this month. budget-summary is a single literal segment after
	// /pipelines/ (depth 1) vs {slug}/budget's depth 2, so it can't
	// collide with the per-slug route the way /pipelines/runs/... would
	// (see the comment below on /pipeline-runs/).
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/budget-summary", authed(wsCtx(http.HandlerFunc(pipes.GetBudgetSummary))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget", authed(wsCtx(http.HandlerFunc(pipes.GetBudget))))
	r.authedMut("PATCH", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget", roleManage, pipes.SetBudget)
	// Single-run + workspace-list lookups under /pipeline-runs/ (top-
	// level resource) instead of /pipelines/runs/ because the latter
	// collides with /pipelines/{slug}/runs in net/http's pattern-
	// matcher: both resolve "/pipelines/runs/runs" without a tie-
	// breaker. Workspace list also stays out of /pipelines/{slug}/
	// because it spans every pipeline.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs", authed(wsCtx(http.HandlerFunc(pipes.ListWorkspaceRuns))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}", authed(wsCtx(http.HandlerFunc(pipes.GetRun))))
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/tree", authed(wsCtx(http.HandlerFunc(pipes.GetRunTree))))
	r.authedMut("PATCH", "/api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/metadata", roleCreate, pipes.UpdateRunMetadata)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/signal", roleCreate, pipes.SignalRun)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/logs", authed(wsCtx(http.HandlerFunc(pipes.RunLogs))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/cancel", roleManage, pipes.CancelRun)
	// Observability (replay-with-original-inputs pattern): replay a failed run
	// with its original inputs, bulk-replay a fingerprint group, and list
	// failures bucketed by fingerprint. errors/bulk_replay are registered before
	// the {runId} replay so the literal segments win net/http matching.
	// Deferred dispatch (delay/ttl/debounce/priority) — list + cancel
	// parked triggers. Registered before {slug} routes so the literal
	// "pending" segment wins net/http matching.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/pending", authed(wsCtx(http.HandlerFunc(pipes.ListPendingRuns))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel", roleCreate, pipes.CancelPendingRun)
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipelines/runs/errors", authed(wsCtx(http.HandlerFunc(pipes.ListErrorGroups))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay", roleCreate, pipes.BulkReplayRuns)
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay", roleCreate, pipes.ReplayRun)
	// Pipeline webhooks — event-driven trigger surface alongside
	// cron schedules. CRUD requires auth; the public dispatch
	// endpoint authenticates via the token + optional HMAC instead.
	r.mux.Handle("GET /api/v1/workspaces/{workspaceId}/pipeline-webhooks", authed(wsCtx(http.HandlerFunc(pipes.ListWebhooks))))
	r.authedMut("POST", "/api/v1/workspaces/{workspaceId}/pipeline-webhooks", roleCreate, pipes.CreateWebhook)
	r.authedMut("DELETE", "/api/v1/workspaces/{workspaceId}/pipeline-webhooks/{webhookId}", roleManage, pipes.DeleteWebhook)
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
