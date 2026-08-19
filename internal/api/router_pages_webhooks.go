package api

// Inbound panel webhooks — route registration (docs/prd/pages.md §10b.5c).
//
// ⚠ THE `router_` PREFIX ON THIS FILENAME IS LOAD-BEARING. Two build gates glob
// exactly `internal/api/router_*.go` — cmd/gen-openapi/main.go:97, which is why
// these routes appear in openapi.gen.json and therefore in the API docs, and
// route_authz_invariant_test.go:88, whose source guard makes registering a
// mutation through a non-recording wrapper a build failure. A registrar under
// any other name silently opts out of both; #1953 lost four mutation routes
// exactly that way.
//
// ── The URL, and why it is not the one §10b.5c prints ──────────────────────
//
// The PRD names `POST /api/v1/pages/webhooks/{token}`. That pattern cannot be
// registered on this mux, and the failure is not subtle — it is a panic at
// registration, i.e. the server does not boot:
//
//	POST /api/v1/pages/webhooks/{token} and POST /api/v1/pages/{slug}/public
//	both match some paths, like "/api/v1/pages/webhooks/public".
//	But neither is more specific than the other.
//
// Go's ServeMux (1.22+) refuses two patterns that overlap when neither is a
// strict subset of the other. `{token}` at position five overlaps `public` at
// position five; `webhooks` at position four overlaps `{slug}` at position
// four. The same collision exists against POST /api/v1/pages/{slug}/rollback,
// and a fresh one would appear the day anybody adds POST
// /api/v1/pages/{slug}/<anything>.
//
// Go documents a fix — register a third, more specific literal
// (POST /api/v1/pages/webhooks/public) — and it was rejected here. It would
// leave a landmine for every future Pages route: adding an ordinary
// POST /api/v1/pages/{slug}/foo would take the server down at boot until
// somebody also added POST /api/v1/pages/webhooks/foo, wired to the {slug}
// handler with a hand-set PathValue. That is a trap with a one-line trigger and
// a five-line disarm, in a feature five agents are building in parallel.
//
// So the inbound endpoint gets its OWN top-level space, which is the shape
// every other token-addressed door in this codebase already has:
//
//	POST /api/v1/webhooks/{token}        a pipeline webhook (router_pipelines.go)
//	GET  /api/v1/public/pages/{token}    a published page   (router_pages_public.go)
//	POST /api/v1/page-webhooks/{token}   this
//
// The hyphenated noun matches the table (`page_webhooks`) and the existing
// `pipeline-webhooks` spelling, and — the property that matters — a path whose
// first segment is not `pages` cannot collide with the {slug} family at all,
// now or later. PageWebhookPath (pages_webhooks.go) is the single place the
// shape is written down; the CLI prints what the server returns rather than
// composing its own, so the two cannot drift.
//
// ── What is authenticated, and what is not ─────────────────────────────────
//
// The inbound route registers on r.mux directly, with no auth wrapper. That is
// the same trust boundary as pipeline webhooks, waitpoint tokens and bootstrap,
// which route_authz_invariant_test.go names as "out of scope by design" because
// they are "uniformly mediated by their own single wrapper" — here, the token
// lookup in FireWebhook: a SHA-256 digest against a unique index, a revoked_at
// check, the panel's own rate limits, and the issuer's live authority
// re-derived on every request. It therefore needs no scopeForRoute case, and
// must not have one: there is no workspace role in the request to gate on.
//
// The three MANAGEMENT routes are ordinary authenticated Pages routes and go
// through the recording wrappers. They reuse the roleSelf sentinel that
// PATCH/DELETE /pages/{slug} and the publish surface already use, for the same
// reason: a page is a per-object ACL (§7.2), its owner may be a MEMBER, and
// only the handler can answer "is this caller the owner" — or, for issuing,
// "is this caller a human at all", which middleware that knows roles cannot
// answer. scopeForRoute's existing `pages` case covers all three
// (workspace:admin); no change to rbac_routes.go is needed.
//
// The golden is regenerated with
//
//	go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles
//
// and the OpenAPI document with `go generate ./internal/api/`.

import "net/http"

func (r *Router) registerPageWebhookRoutes() {
	// Its own handler instance, following registerPageActionRoutes and
	// registerPagePublicRoutes: the group is self-contained, and it is also what
	// lets cmd/gen-openapi resolve `p` back to PageHandler and read the statuses
	// these operations actually write. A handler passed in as a parameter is
	// invisible to that resolution, and the published contract then claims a
	// bare 200.
	//
	// The one piece of state it holds is §10b.3's push-rate buckets, and they
	// are per-instance on purpose — the same caveat pages_internal.go records:
	// they are per-PROCESS anyway (config/rate-limits.yml's own header), so
	// sharing them between route groups would buy a guarantee a second replica
	// takes straight back. What holds across every door and across replicas is
	// the floor in the push transaction, because the ROW is shared.
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())

	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// ── The inbound surface. No auth wrapper, by design. ───────────────────
	r.mux.HandleFunc("POST /api/v1/page-webhooks/{token}", p.FireWebhook)

	// ── The issuer's surface. Authenticated, and create is human-only. ─────
	r.mux.Handle("GET /api/v1/pages/{slug}/webhooks", authed(wsCtx(http.HandlerFunc(p.ListWebhooks))))
	r.authedMut("POST", "/api/v1/pages/{slug}/webhooks", roleSelf, p.CreateWebhook)
	r.authedMut("DELETE", "/api/v1/pages/{slug}/webhooks/{webhookId}", roleSelf, p.RevokeWebhook)
}
