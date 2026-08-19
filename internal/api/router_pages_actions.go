package api

// Pages action routes (docs/prd/pages.md §8b.2, §11).
//
// ⚠ The `router_` prefix on this filename is load-bearing. Two build gates glob
// exactly `router_*.go` — cmd/gen-openapi/main.go:97, which is why these routes
// appear in openapi.gen.json and therefore in the API docs, and
// route_authz_invariant_test.go:88, which is what makes registering a mutation
// through a non-recording wrapper a build failure. A registrar under any other
// name silently opts out of both; #1953 lost four mutation routes that way.
//
// Why POST is roleCreate and not roleSelf, when every other Pages mutation is
// roleSelf: the other five answer a per-object question the middleware cannot
// (ownership, a grant, producer authority), so they have to reach the handler.
// This one has a FLOOR the middleware can enforce and should — dispatching an
// action runs a routine, and running a routine is MANAGER+ on its own endpoint
// (pipelines_exec.go:71). Declaring that floor here means a page button can
// never be a cheaper way to run a routine than the routine's own surface, and
// it keeps the route's CLI-token scope at `workspace:admin` (scopeForRoute's
// `pages` case) instead of the scope exemption roleSelf carries. The per-object
// half — can this caller SEE the panel — is the handler's, and refuses with 404
// rather than 403 so a sealed panel is not enumerable (pages_actions.go).
//
// The golden is regenerated with
//
//	go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles
//
// and the OpenAPI document with `go generate ./internal/api/`.

import "net/http"

func (r *Router) registerPageActionRoutes() {
	// Its own handler, following registerPageTransferRoutes: the group is
	// self-contained, and it is also what lets cmd/gen-openapi resolve `p` back
	// to PageHandler and read the statuses these two operations actually write.
	// A handler passed in as a parameter is invisible to that resolution, and
	// the published contract then claims a bare 200.
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())

	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	r.mux.Handle("GET /api/v1/pages/{slug}/panels/{panelId}/actions",
		authed(wsCtx(http.HandlerFunc(p.ListPanelActions))))
	r.authedMut("POST", "/api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}",
		roleCreate, p.DispatchAction)
}
