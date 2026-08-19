package api

// Route registration for the Pages portability and history surface
// (docs/prd/pages.md §10b.1, §10b.2).
//
// It is a separate file from router_pages.go for the reason pages_internal.go
// gives for its own registration — the group is self-contained, and a
// registration line whose handler is three files away is how a route ends up
// outliving the thing it served.
//
// THE NAME IS LOAD-BEARING. Two build-time invariants find route registrations
// by globbing `router_*.go`: cmd/gen-openapi (which is why the four routes
// below reach openapi.gen.json) and route_authz_invariant_test.go's source
// guard, which fails the build when a mutation registers the old
// `authed(...)` way instead of through authedMut. A file called
// pages_transfer_routes.go compiles and serves identically and is invisible to
// both — an opt-out from a security gate by filename. Anything registering
// routes belongs under the prefix the guards look for.
//
// The same CI-gated constraints apply as to every other mutation route:
// authedMut (never bare authed), a scopeForRoute case (the `pages` family
// already resolves to workspace:admin), and the golden regenerated with
//
//	go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles
//
// Role declarations:
//
//	POST /api/v1/pages/import          roleInline — importing CREATES a page,
//	                                   so it is Create's gate, capability layer
//	                                   included: a MEMBER holding page.create
//	                                   installs a bundle without a promotion.
//	POST /api/v1/pages/{slug}/rollback roleSelf   — a rollback is an edit of the
//	                                   arrangement, so it is PATCH's gate:
//	                                   ownership, workspace admin, or a `write`
//	                                   grant, all of which only the handler can
//	                                   decide (§7.2's per-object ACL).
//
// The two reads register as plain authed(wsCtx(...)) like every other GET in
// this package, and both then run the `write` test in the handler — an export
// and a version both carry the WHOLE arrangement, including panels an ordinary
// reader receives sealed (§7.1 rule 2). See Export's doc comment.
//
// One naming collision is knowingly accepted, following pipelines
// (POST …/pipelines/import): a page may legally be slugged `import`, and
// `POST /api/v1/pages/import` would then read as that page's address. Nothing
// breaks, because there is no POST /api/v1/pages/{slug} route to be shadowed —
// pages are created at the collection and edited with PATCH.

import "net/http"

func (r *Router) registerPageTransferRoutes() {
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())

	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	r.mux.Handle("GET /api/v1/pages/{slug}/export", authed(wsCtx(http.HandlerFunc(p.Export))))
	r.mux.Handle("GET /api/v1/pages/{slug}/versions", authed(wsCtx(http.HandlerFunc(p.ListVersions))))
	r.authedMut("POST", "/api/v1/pages/import", roleInline, p.Import)
	r.authedMut("POST", "/api/v1/pages/{slug}/rollback", roleSelf, p.Rollback)
}
