package server

import (
	"net/http"
	"path"
	"strings"

	"github.com/crewship-ai/crewship/internal/editororigin"
	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

// securityHeadersMiddleware adds standard security headers to all HTTP responses.
// CSP is path-aware:
//   - SPA UI: looser policy with 'unsafe-inline' / 'unsafe-eval' for the Next.js
//     runtime; connect-src 'self' covers same-origin WebSocket.
//   - /openapi (the browsable spec rendering): default-src 'none' with 'self'
//     for its one stylesheet and one script, and nothing else — see #1846.
//   - API / health endpoints: strict default-src 'none'. Same baseline is
//     reapplied by api.SecurityHeaders inside the API router so the policy
//     survives if any future surface is reached without going through this
//     wrapper.
//   - /exposed/: NO CSP header. That route is the reverse proxy for port-
//     exposed user apps — the upstream owns its own policy. api.SecurityHeaders
//     also matches this path and skips CSP so the API router doesn't re-stamp
//     the lockdown after this middleware bowed out.
func securityHeadersMiddleware(next http.Handler, pageRuntimeOrigin ...string) http.Handler {
	var o securityHeaderOptions
	if len(pageRuntimeOrigin) > 0 {
		o.pageRuntimeOrigin = pageRuntimeOrigin[0]
	}
	return securityHeadersMiddlewareWith(next, o)
}

// securityHeaderOptions are the operator-configured origins Studio may frame.
type securityHeaderOptions struct {
	pageRuntimeOrigin string
	// pageRuntimeLoopbackOnly: pageRuntimeOrigin was derived for a localhost
	// Studio (#3108), so only documents served on a loopback host may frame
	// it; a Studio reached on any other host gets no loopback frame-src.
	pageRuntimeLoopbackOnly bool
	// editorFrameSrc returns the embedded workbook editor's frame-src source
	// for a request; nil while the editor is off. It is a dedicated editor
	// origin, or the bundle path on the request's Studio origin (zero-config
	// default, never the bare Studio origin, so a self-navigated editor frame
	// cannot reach a Studio document). It is added to frame-src of every UI
	// document: the policy belongs to the document, and the UI routes on the
	// client, so a document first loaded on /chat may later show a workbook
	// Page. The editor document is CSP-sandboxed and holds no credential;
	// frame-src governs only what Studio frames, not who may frame Studio
	// (frame-ancestors stays 'none').
	editorFrameSrc func(*http.Request) string
	// editorPerRequest: the source depends on the request's Host and
	// scheme, so UI documents are kept out of shared caches.
	editorPerRequest bool
}

func appendFrameSrc(frameSrc, origin string) string {
	if origin == "" {
		return frameSrc
	}
	if frameSrc == "frame-src 'none'" {
		return "frame-src " + origin
	}
	return frameSrc + " " + origin
}

func securityHeadersMiddlewareWith(next http.Handler, o securityHeaderOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorFrameSrc := ""
		if o.editorFrameSrc != nil {
			editorFrameSrc = o.editorFrameSrc(r)
		}
		runtimeFrameSrc := o.pageRuntimeOrigin
		if o.pageRuntimeLoopbackOnly && !pagebuild.LoopbackHost(r.Host) {
			runtimeFrameSrc = ""
		}
		frameSrc := appendFrameSrc(appendFrameSrc(pages.FrameSrcDirective(), runtimeFrameSrc), editorFrameSrc)
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")

		// Audit M5: Content-Security-Policy. The SPA bundle from Next.js
		// uses inline runtime hydration ('unsafe-inline' style, plus an
		// inline boot script), so the UI policy is permissive on those
		// axes but tight on script-src/connect-src. Non-UI surfaces
		// (API JSON, health probes) get the "default-src 'none'"
		// lockdown so a Content-Type mishap can't turn into an XSS.
		//
		// /exposed/ is the reverse-proxy path for port-exposed apps —
		// the upstream may serve arbitrary HTML/JS that needs its own
		// policy. We DON'T set CSP, HSTS, COEP, or CORP on those
		// responses; if the upstream returns these headers they
		// propagate as-is, and if not the browser default applies.
		// Stamping them would credentialless-strip the upstream's own
		// no-cors fetches and same-origin-clamp resources it expects
		// to be embeddable cross-origin — both behaviour changes the
		// proxied app didn't opt into.
		// CodeRabbit flagged the CSP case in PR #236; the COEP/CORP
		// scoping landed alongside in PR #551 after re-review.
		path := r.URL.Path
		isExposed := strings.HasPrefix(path, "/exposed/")
		// UI = anything the SPA handler serves; the mux-routed surfaces
		// (API, /exposed/, probes, WebSockets) share one predicate with
		// combinedHandler so the two lists can't drift.
		isUI := !isMuxRoutedPath(path)

		if !isExposed {
			// Audit 2026-05-23: ZAP flagged missing HSTS, COEP, CORP.
			//
			// HSTS: 1-year max-age + includeSubDomains. preload
			// omitted on purpose — opting into Chrome's preload list
			// is a one-way door (removal takes months) and requires
			// every current and future subdomain to be HTTPS-only.
			// The same-host workbook editor sends this exact value too.
			w.Header().Set("Strict-Transport-Security", editororigin.StudioHSTS)

			// COEP `credentialless` strips cookies from cross-origin
			// no-cors requests so the page can opt into cross-origin
			// isolation without breaking CDN images that lack CORP
			// headers. `require-corp` would break any cross-origin
			// subresource that does not explicitly send a CORP header
			// — too aggressive for a SPA that may surface user-
			// supplied avatar URLs or third-party badges.
			w.Header().Set("Cross-Origin-Embedder-Policy", "credentialless")

			// CORP: this origin's own resources should never be
			// embedded from a different origin (no cross-origin
			// <img>, <script>, <iframe> targeting our UI/API). Pairs
			// with frame-ancestors 'none' in strict CSP + COOP above.
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		}

		switch {
		case isExposed:
			// Upstream owns its own policy; do not stamp.
		case isOpenAPIDocsPath(path):
			// The browsable OpenAPI rendering (#1846) is HTML served by the
			// mux, so it needs its own stylesheet and its filter script —
			// but nothing else, and nothing from anywhere else. Keeping
			// 'self' as the only source is what makes the page's offline
			// guarantee enforceable by the browser rather than by review:
			// a CDN bundle or a remote font added here later does not load.
			// No 'unsafe-inline' either, which is why the renderer emits no
			// inline <script> and no style="" attribute.
			w.Header().Set("Content-Security-Policy",
				"default-src 'none'; "+
					"script-src 'self'; "+
					"style-src 'self'; "+
					"img-src 'self' data:; "+
					"font-src 'self'; "+
					"form-action 'none'; "+
					"frame-ancestors 'none'; "+
					"base-uri 'none'")
		case isUI:
			// connect-src is just 'self' — earlier 'ws: wss:' was a
			// scheme-only source that allows WebSocket to ANY host;
			// 'self' covers same-origin ws/wss, which is what we want.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
					"style-src 'self' 'unsafe-inline'; "+
					// logos.composio.dev serves the managed-integration brand
					// logos (Gmail, GitHub, Slack, …) shown in the connector
					// catalog. Scoped to that one host — not a blanket https:.
					"img-src 'self' data: blob: https://logos.composio.dev; "+
					"font-src 'self' data:; "+
					"connect-src 'self'; "+
					// Pages' embed.v1 panel (docs/specs/pages.md §3.1). Without
					// this directive frame-src falls back to default-src
					// 'self', so a correctly configured, human-vetted embed
					// source is still refused by the browser — the panel would
					// render an empty frame and no error anyone could act on.
					//
					// The value is derived from the operator's allow-list and
					// is "frame-src 'none'" when nothing is configured, which
					// is every instance that has not opted in. Nothing a
					// producer sends can widen it: a payload names a source,
					// the policy names the origins.
					frameSrc+"; "+
					"frame-ancestors 'none'; "+
					"base-uri 'self'; "+
					"form-action 'self'")
			if (o.editorPerRequest || (o.pageRuntimeLoopbackOnly && o.pageRuntimeOrigin != "")) && isUIDocumentPath(path) {
				// The same-host editor's frame-src names this request's own
				// origin. Caches key on host and scheme, but one that maps
				// several hosts to one entry would replay another host's
				// policy: documents stay out of shared caches. Hashed static
				// assets carry no policy that matters and stay cacheable.
				// The UI handler sets its own Cache-Control, so "private" is
				// merged in when the response is written, not before.
				w = &privateCacheWriter{ResponseWriter: w}
			}
		default:
			w.Header().Set("Content-Security-Policy",
				"default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		}

		next.ServeHTTP(w, r)
	})
}

// isUIDocumentPath reports whether a UI path can serve an HTML document: no
// file extension (client routes) or .html.
func isUIDocumentPath(p string) bool {
	ext := path.Ext(p)
	return ext == "" || ext == ".html"
}

// privateCacheWriter makes the response's Cache-Control private when its
// header is written, whatever the wrapped handler set.
type privateCacheWriter struct {
	http.ResponseWriter
	done bool
}

func (p *privateCacheWriter) mark() {
	if p.done {
		return
	}
	p.done = true
	h := p.ResponseWriter.Header()
	h.Set("Cache-Control", privateCacheControl(h.Get("Cache-Control")))
}

func (p *privateCacheWriter) WriteHeader(code int) {
	p.mark()
	p.ResponseWriter.WriteHeader(code)
}

func (p *privateCacheWriter) Write(b []byte) (int, error) {
	p.mark()
	return p.ResponseWriter.Write(b)
}

func (p *privateCacheWriter) Flush() {
	p.mark()
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (p *privateCacheWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// privateCacheControl keeps a Cache-Control value's directives but makes
// it private: "public" is dropped and "private" added unless the response
// is already private or no-store.
func privateCacheControl(v string) string {
	var out []string
	private := false
	for _, d := range strings.Split(v, ",") {
		d = strings.TrimSpace(d)
		name, _, _ := strings.Cut(strings.ToLower(d), "=")
		switch name {
		case "":
			continue
		case "public":
			continue
		case "private", "no-store":
			private = true
		}
		out = append(out, d)
	}
	if !private {
		out = append([]string{"private"}, out...)
	}
	return strings.Join(out, ", ")
}
