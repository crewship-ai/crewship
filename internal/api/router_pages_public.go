package api

// Public pages — route registration (docs/prd/pages.md §7.3.1).
//
// "A public page is served from a SEPARATE URL SPACE (/p/{token}) that shares
// no session, no cookie and no workspace context with the app. Nothing about it
// is 'the same page with a looser grant' — it is a distinct rendering path with
// its own middleware, and that separation is what makes it auditable."
//
// That is why this registrar exists at all rather than three more lines in
// router_pages.go. The split is the auditable property: every route below is
// either unauthenticated by design and reachable only with a 256-bit token, or
// authenticated and gated on being human. Nothing in between.
//
// THE FILE NAME IS LOAD-BEARING. Two tools glob `internal/api/router_*.go` and
// only that: cmd/gen-openapi, which generates openapi.gen.json (and whose
// conformance test fails the build for a route the spec does not document), and
// route_authz_invariant_test.go, whose source guard fails the build for a
// mutation registered through the legacy `authed(...)` chain. A registrar named
// anything else is invisible to both — it does not error, it silently opts out
// of two build-time invariants. This file was briefly called
// pages_public_routes.go and did exactly that.
//
// WHY NONE OF THIS TOUCHES rbac_routes.go.
// The two unauthenticated routes are a public token surface, the same trust
// boundary as webhooks, waitpoint tokens and bootstrap — which
// route_authz_invariant_test.go names in as many words as "out of scope by
// design", because they are "uniformly mediated by their own single wrapper"
// rather than by a workspace role. They therefore register through r.mux
// directly and need no scopeForRoute case. The three AUTHENTICATED routes do go
// through the recording wrappers, and they reuse the roleSelf sentinel that
// PATCH/DELETE /pages/{slug} already use for the same reason: a page is a
// per-object ACL (§7.2), its owner may be a MEMBER, and only the handler can
// answer "is this caller the owner" — or, for publishing, "is this caller a
// human at all", which middleware that knows roles cannot answer.
//
// /p/{token} ITSELF IS NOT REGISTERED HERE, and that is not an omission. It is
// the FRONTEND route (app/p/[token]/), served as a static export by the SPA
// handler like every other page in the product; the data it renders comes from
// GET /api/v1/public/pages/{token} below. Registering /p/ on the mux would mean
// teaching internal/server's combinedHandler that one more prefix bypasses the
// SPA, for a page that has no server-rendered content to serve.

import "net/http"

func (r *Router) registerPagePublicRoutes() {
	base := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())
	pub := NewPagePublicHandler(base)

	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// ── The public surface. No auth wrapper, by design. ────────────────────
	//
	// The token in the path IS the credential (§7.3.1) and the handler is the
	// only thing that validates it: it is looked up by SHA-256 hash, checked
	// against revoked_at and expires_at, rate-limited per token, and — when the
	// link carries one — gated on a bcrypt password that arrives in a POST body
	// and never in the URL (§7.3.3).
	r.mux.Handle("GET /api/v1/public/pages/{token}", http.HandlerFunc(pub.View))
	r.mux.Handle("POST /api/v1/public/pages/{token}/unlock", http.HandlerFunc(pub.Unlock))

	// ── The owner's surface. Authenticated, and publish is human-only. ─────
	r.mux.Handle("GET /api/v1/pages/{slug}/public", authed(wsCtx(http.HandlerFunc(base.ListPublicLinks))))
	r.authedMut("POST", "/api/v1/pages/{slug}/public", roleSelf, base.Publish)
	r.authedMut("DELETE", "/api/v1/pages/{slug}/public/{tokenId}", roleSelf, base.RevokePublicLink)
}
