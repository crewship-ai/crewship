package server

import (
	"net/http"
	"strings"
)

// pathTraversalRejectMiddleware returns 400 for any request whose URL
// path contains a `..` segment. The default net/http.ServeMux otherwise
// normalises `/foo/../bar` → `/bar` and emits a 301 with the resolved
// path in the Location header -- which leaks the route back to the
// caller. An attacker probing `/api/v1/admin/../something` gets
// confirmation of `/api/v1/something`'s existence (or non-existence)
// from the 301 + Location, which is useful intel for follow-up
// payloads even when the resolved endpoint itself 403s. Audit M11.
//
// The check looks for literal ".." path segments (split on /) rather
// than `strings.Contains(path, "..")` so a benign filename or slug
// such as `two..dots.txt` still serves -- only the traversal-shaped
// `/foo/../bar` pattern is rejected.
//
// Placed inside securityHeadersMiddleware so the 400 response still
// carries the standard security headers (X-Frame-Options et al.).
func pathTraversalRejectMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, seg := range strings.Split(r.URL.Path, "/") {
			if seg == ".." {
				http.Error(w, "Bad Request", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
