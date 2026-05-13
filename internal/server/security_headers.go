package server

import (
	"net/http"
	"strings"
)

// securityHeadersMiddleware adds standard security headers to all HTTP responses.
// CSP is path-aware:
//   - SPA UI: looser policy with 'unsafe-inline' / 'unsafe-eval' for the Next.js
//     runtime; connect-src 'self' covers same-origin WebSocket.
//   - API / health endpoints: strict default-src 'none'. Same baseline is
//     reapplied by api.SecurityHeaders inside the API router so the policy
//     survives if any future surface is reached without going through this
//     wrapper.
//   - /exposed/: NO CSP header. That route is the reverse proxy for port-
//     exposed user apps — the upstream owns its own policy. api.SecurityHeaders
//     also matches this path and skips CSP so the API router doesn't re-stamp
//     the lockdown after this middleware bowed out.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// policy. We DON'T set CSP on those responses; if the upstream
		// returns a CSP header it propagates as-is, and if not, the
		// browser default applies. CodeRabbit flagged this in PR #236.
		path := r.URL.Path
		isExposed := strings.HasPrefix(path, "/exposed/")
		isUI := !isExposed &&
			!strings.HasPrefix(path, "/api/") &&
			path != "/healthz" && path != "/readyz" &&
			path != "/metrics" && path != "/ws" && path != "/ws/terminal"
		switch {
		case isExposed:
			// Upstream owns its own policy; do not stamp.
		case isUI:
			// connect-src is just 'self' — earlier 'ws: wss:' was a
			// scheme-only source that allows WebSocket to ANY host;
			// 'self' covers same-origin ws/wss, which is what we want.
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: blob:; "+
					"font-src 'self' data:; "+
					"connect-src 'self'; "+
					"frame-ancestors 'none'; "+
					"base-uri 'self'; "+
					"form-action 'self'")
		default:
			w.Header().Set("Content-Security-Policy",
				"default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		}

		next.ServeHTTP(w, r)
	})
}
