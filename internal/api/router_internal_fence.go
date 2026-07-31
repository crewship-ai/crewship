package api

import (
	"net/http"
	pathpkg "path"
	"strings"
)

// internalPathPrefix is the sidecar-only IPC surface. Every route registered in
// router_internal.go lives under it, and nothing else does.
const internalPathPrefix = "/api/v1/internal/"

// isInternalPath reports whether path addresses the internal surface, under any
// spelling ServeMux would canonicalise onto it.
//
// The raw path is not enough. ServeMux cleans a path before matching, so
// `//api/v1/internal/credentials`, `/api/v1//internal/credentials` and
// `/api/v1/./internal/credentials` all reach the same route — but none of them
// has "/api/v1/internal/" as a literal prefix. Testing the raw path alone would
// route those spellings past serveInternal into the mux, which answers a
// non-canonical path with a 307 whose Location is the cleaned path. That is a
// second door onto the surface, and it echoes back the canonical route.
func isInternalPath(path string) bool {
	return strings.HasPrefix(path, internalPathPrefix) ||
		strings.HasPrefix(cleanURLPath(path), internalPathPrefix)
}

// serveInternal is the single door onto /api/v1/internal/*.
//
// requireInternal answers 404 — not 401/403 — for a caller that fails the
// network-origin gate, precisely so the internal surface is indistinguishable
// from routes that do not exist. That fence works for ACCESS and used to be
// bypassed for EXISTENCE (#1501): http.ServeMux matches the pattern's PATH and
// rejects the METHOD before the handler, and therefore before any middleware,
// runs. So `GET /api/v1/internal/keeper/request` — a POST-only route — returned
// 405 with an `Allow: POST` header straight from the mux, and an
// unauthenticated prober could map the credential broker, the Keeper request
// path and the agent registry without ever being let in. A path that does not
// exist answered a plain-text 404, so "405 vs 404" and even
// "text/plain 404 vs JSON 404" were both positive signals.
//
// The fix is a chokepoint rather than a per-route rule, because a per-route rule
// is exactly the kind of thing the next route added forgets: this function is
// the one place routeWithRateLimiting hands the internal prefix to the mux, so
// every internal route — present and future — passes through it by
// construction. It asks the mux whether it would DISPATCH the request to a real
// handler; if not, for any reason, the caller gets the same response
// requireInternal produces for an unauthorised call. Method mismatch, unknown
// path and refused origin are then one answer with one body and no `Allow`
// header, at the same cost (the decision is a routing-table lookup either way —
// no credential compare, no DB round-trip, so there is no timing tell either).
//
// What it deliberately does NOT do is rewrite responses. A handler that reached
// its own 404 ("agent not found") keeps its body, because sidecars parse those.
// Only requests the mux itself would have refused are answered here.
//
// Residual signal, stated plainly: because the canonical 404 is JSON and the
// mux's generic one is text, a prober can still tell that the /api/v1/internal/
// prefix exists. That is public knowledge — it is in the docs, the OpenAPI
// generator excludes it by name, and the edge denies it by name. What is closed
// is the map of which paths under it are real.
func (r *Router) serveInternal(w http.ResponseWriter, req *http.Request) {
	if !r.internalRouteDispatches(req) {
		// Byte-identical to requireInternal's refusal (replyError → writeJSON):
		// status, Content-Type and body must all match, or the difference is
		// the signal we just removed.
		replyError(w, http.StatusNotFound, "Not Found")
		return
	}
	r.mux.ServeHTTP(w, req)
}

// internalRouteDispatches reports whether r.mux would hand req to a registered
// handler, as opposed to answering it out of its own 404 / 405 defaults.
//
// It delegates to the mux's own matcher rather than re-implementing route
// matching — wildcards ({agentId}), the implicit HEAD-rides-GET rule and
// pattern precedence all have to behave exactly as they do in ServeHTTP, and
// the only way to guarantee that is to ask the same function. (*ServeMux).Handler
// returns an empty pattern string for both the not-found and the
// method-not-allowed cases (net/http findHandler), which is precisely the
// distinction needed here.
//
// The non-canonical path check comes first because ServeMux answers a request
// whose path needs cleaning with a `307` to the cleaned path, and it reports
// the CLEANED path's pattern — so the redirect would both dispatch and echo the
// canonical internal route back to the caller. The internal surface has no use
// for path-cleaning redirects; sidecars build their URLs from constants.
// (isInternalPath is what makes sure such a request arrives here at all.)
//
// One ServeMux redirect is NOT covered here: the `/tree` → `/tree/` rule, which
// fires when a SUBTREE pattern (one ending in `/`) is registered and the request
// omits the trailing slash. net/http reports the subtree pattern for it, so this
// function would call it a dispatch and the mux would answer `307` — an
// existence signal. No internal route is registered that way today, and
// TestInternalRoutes_NoSubtreePatterns fails the build if one ever is, which is
// the cheaper guard: the distinction is invisible from the returned pattern
// (a subtree pattern legitimately serves paths that do not end in `/`).
func (r *Router) internalRouteDispatches(req *http.Request) bool {
	if escaped := req.URL.EscapedPath(); escaped != cleanURLPath(escaped) {
		return false
	}
	_, pattern := r.mux.Handler(req)
	return pattern != ""
}

// cleanURLPath mirrors net/http's unexported cleanPath: the canonical form of a
// URL path, with the trailing slash preserved.
func cleanURLPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := pathpkg.Clean(p)
	// path.Clean drops a trailing slash; put it back so "/a/b/" stays distinct
	// from "/a/b" (ServeMux treats them as different patterns).
	if p[len(p)-1] == '/' && np != "/" {
		np += "/"
	}
	return np
}
