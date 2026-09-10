package api

// Pages route registration (docs/prd/pages.md §11).
//
// The routes are WORKSPACE-UNSCOPED — /api/v1/pages/... with wsCtx supplying
// the workspace (§11b decision 1), following saved-views, missions, runs,
// journal and automations. Pipelines' scoped shape
// (/api/v1/workspaces/{ws}/pipelines) is the older pattern, and the CLI
// already appends workspace_id (internal/cli/client.go).
//
// Registration constraints, all CI-gated (§11 and rbac_routes.go):
//
//   - Mutations register through authedMut, never bare authed(...) —
//     route_authz_invariant_test.go fails the build otherwise.
//   - Each pattern needs a scopeForRoute case, or the scope resolves to ""
//     and the enumeration invariant fails the build.
//   - The golden is regenerated with
//     `go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles`.
//
// Why three of the four mutations are roleSelf and one is roleInline:
//
//	POST   /pages          roleInline — the v109 layered gate. A MEMBER holding
//	                       the page.create capability passes; gating on the
//	                       plain workspace role in middleware would wrongly
//	                       refuse them, which is exactly what roleInline exists
//	                       to avoid.
//	PATCH  /pages/{slug}   roleSelf   — ownership, or a `write` grant, decided
//	DELETE /pages/{slug}   roleSelf     in the handler. A page is a per-object
//	                       ACL (§7.2), and the workspace role is not the whole
//	                       answer for either verb.
//	PUT    …/panels/…/data roleSelf   — producer authority (§7.1 rule 4) is not
//	                       a workspace role at all: the declared producer, or a
//	                       `produce` grant, is what decides, and a MEMBER
//	                       holding one must pass.
//	PUT    …/grants        roleSelf   — a page is a per-object ACL (§7.2) and
//	DELETE …/grants          its owner may be a MEMBER. §7.1 rule 3 gives the
//	                       verb to "the page owner or a workspace ADMIN/OWNER",
//	                       and only the handler can answer the first half.
//	                       §7.1b rule 1 — only a human issues a grant — is
//	                       likewise the handler's: the middleware knows roles,
//	                       not whether a container is holding the pen.

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

func (r *Router) registerPageRoutes() {
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())
	// Held on the Router so cmd_start can reach THIS instance: it owns the
	// create/update/delete paths that write wake-gate rules, and the freshness
	// sweeper has to run against the same clock and journal.
	r.pages = p
	if r.pageProjectsPath != "" {
		p.SetProjectStore(&pages.ProjectStore{Directory: r.pageProjectsPath})
	}

	if r.pageBuildImage != "" && r.pageProjectsPath != "" {
		if err := pagebuild.ValidateImage(r.pageBuildImage); err != nil {
			r.logger.Error("Page build configuration", "error", err)
		} else {
			p.SetBuildWorker(&pagebuild.DockerBuilder{Image: r.pageBuildImage}, &pagebuild.Store{Directory: filepath.Join(r.pageProjectsPath, "artifacts")})
			if err := p.recoverPageBuilds(context.Background()); err != nil {
				r.logger.Error("recover Page builds", "error", err)
				p.builds = nil
			}
		}
	}
	if r.pageRuntimeOrigin != "" {
		if err := pagebuild.ValidateRuntimeOriginForDevelopment(r.pageRuntimeOrigin, r.pageStudioOrigin, r.pageRuntimeDevelopmentSameOrigin); err != nil {
			r.logger.Error("Page runtime configuration", "error", err)
		} else {
			p.pageRuntimeOrigin = r.pageRuntimeOrigin
			p.pageStudioOrigin = r.pageStudioOrigin
			p.pageRuntimeDevelopmentSameOrigin = r.pageRuntimeDevelopmentSameOrigin
			if r.pageRuntimeDevelopmentSameOrigin {
				r.logger.Warn("Pages same-origin development mode enabled: reviewed code only, no browser process isolation guarantee")
			}
		}
	}
	// Same two wrappers every read route in this package uses; the local
	// aliases keep the registration lines readable (router_orchestration.go).
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	r.mux.Handle("GET /api/v1/pages", authed(wsCtx(http.HandlerFunc(p.List))))
	r.mux.Handle("GET /api/v1/pages/{slug}", authed(wsCtx(http.HandlerFunc(p.Get))))
	r.mux.Handle("GET /api/v1/pages/{slug}/grants", authed(wsCtx(http.HandlerFunc(p.ListGrants))))
	r.authedMut("POST", "/api/v1/pages", roleInline, p.Create)
	r.authedMut("PATCH", "/api/v1/pages/{slug}", roleSelf, p.Update)
	r.authedMut("DELETE", "/api/v1/pages/{slug}", roleSelf, p.Delete)
	r.authedMut("PUT", "/api/v1/pages/{slug}/panels/{panelId}/data", roleSelf, p.PushData)
	r.authedMut("PUT", "/api/v1/pages/{slug}/grants", roleSelf, p.PutGrant)
	r.authedMut("DELETE", "/api/v1/pages/{slug}/grants", roleSelf, p.DeleteGrant)

	// Export/import (§10b.2) and the version history behind `page rollback`
	// (§10b.1) register themselves, next to their handlers — see
	// router_pages_transfer.go.
	r.registerPageTransferRoutes()
	// openapi: responses 200,404
	r.mux.Handle("GET /api/v1/pages/runtime/bootstrap", http.HandlerFunc(p.PageRuntime))
	// openapi: responses 200,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/project", authed(wsCtx(http.HandlerFunc(p.GetProject))))
	// openapi: responses 200,400,401,403,404,409,413,422,500,503,507
	r.authedMut("PUT", "/api/v1/pages/{slug}/project", roleSelf, p.PutProject)
	// openapi: responses 202,400,401,403,404,409,413,429,500,503,507
	r.authedMut("POST", "/api/v1/pages/{slug}/project/build", roleSelf, p.BuildProject)
	// openapi: responses 200,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/project/preview", authed(wsCtx(http.HandlerFunc(p.GetProjectPreview))))
	// openapi: responses 200,400,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/project/fsck", authed(wsCtx(http.HandlerFunc(p.VerifyProjectStorage))))
	r.authedMut("POST", "/api/v1/pages/maintenance", roleManage, p.CompactPageProjects)
	r.mux.Handle("GET /api/v1/pages/{slug}/project/history", authed(wsCtx(http.HandlerFunc(p.ProjectHistory))))
	// openapi: responses 200,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/project/review", authed(wsCtx(http.HandlerFunc(p.ReviewProject))))
	// openapi: responses 200,400,401,403,404,409,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/project/history/{revision}", authed(wsCtx(http.HandlerFunc(p.GetProjectRevision))))
	// openapi: responses 200,400,401,403,404,409,413,422,500,503,507
	r.authedMut("POST", "/api/v1/pages/{slug}/project/restore", roleSelf, p.RestoreProject)

	// openapi: responses 200,400,401,403,404,409,413,422,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/project/check", roleSelf, p.CheckProject)
	// openapi: responses 200,400,401,403,404,409,413,422,500,503,507
	r.authedMut("POST", "/api/v1/pages/{slug}/project/publish", roleSelf, p.PublishProject)
	// openapi: responses 200,400,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/project/publications", authed(wsCtx(http.HandlerFunc(p.PublicationHistory))))
	// openapi: responses 200,400,401,403,404,409,413,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/project/unpublish", roleSelf, p.UnpublishProject)
	// openapi: responses 200,304,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/application", authed(wsCtx(http.HandlerFunc(p.PageApplication))))

	// openapi: responses 202,400,401,403,404,409,413,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/application/actions/{panelId}/{actionId}", roleCreate, p.DispatchApplicationAction)
	// openapi: responses 200,401,403,404,500
	r.mux.Handle("GET /api/v1/pages/{slug}/application/actions/{pendingId}", authed(wsCtx(http.HandlerFunc(p.ApplicationActionStatus))))

	// openapi: responses 200,400,401,404,409,413,500
	r.mux.Handle("GET /api/v1/pages/{slug}/application/panels/{panelId}/history", authed(wsCtx(http.HandlerFunc(p.ApplicationPanelHistory))))

	// Action dispatch (§8b.2) — router_pages_actions.go, which also argues why
	// its POST declares a role floor where the mutations above declare roleSelf.
	r.registerPageActionRoutes()
}
