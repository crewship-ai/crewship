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

import "net/http"

func (r *Router) registerPageRoutes() {
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())

	// Same two wrappers every read route in this package uses; the local
	// aliases keep the registration lines readable (router_orchestration.go).
	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	r.mux.Handle("GET /api/v1/pages", authed(wsCtx(http.HandlerFunc(p.List))))
	r.mux.Handle("GET /api/v1/pages/{slug}", authed(wsCtx(http.HandlerFunc(p.Get))))
	r.authedMut("POST", "/api/v1/pages", roleInline, p.Create)
	r.authedMut("PATCH", "/api/v1/pages/{slug}", roleSelf, p.Update)
	r.authedMut("DELETE", "/api/v1/pages/{slug}", roleSelf, p.Delete)
	r.authedMut("PUT", "/api/v1/pages/{slug}/panels/{panelId}/data", roleSelf, p.PushData)
}
