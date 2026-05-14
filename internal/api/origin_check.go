package api

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// allowedOriginSuffixes is the parsed form of CREWSHIP_ALLOWED_ORIGINS — a
// comma-separated list of origins (e.g. "https://app.crewship.io,
// https://staging.crewship.io"). These are matched as exact-host equality
// against the request's Origin / Referer scheme://host[:port]. Empty means
// "same-origin only" — which we implement by deriving the expected origin
// from the request's Host header.
//
// Why suffixes-but-actually-exact: we used to support trailing-dot tricks
// and bare-host matching here; both turned out to enable bypasses (FQDN
// canonicalisation, IDN, port-mismatch). Exact equality after url.Parse
// is the simplest thing that's actually correct.
var allowedOriginSuffixes = parseAllowedOrigins(os.Getenv("CREWSHIP_ALLOWED_ORIGINS"))

func parseAllowedOrigins(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := []string{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// Normalise: drop trailing slash so "https://app.example.com/"
		// equals "https://app.example.com".
		item = strings.TrimRight(item, "/")
		out = append(out, item)
	}
	return out
}

// methodIsStateChanging reports whether the request might have side
// effects. We enforce the Origin guard on these and let the safer GET /
// HEAD / OPTIONS through — those are also the ones SameSite=Lax already
// permits cross-site, so the policies line up.
func methodIsStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// requestOrigin returns the cross-origin signal we should compare against
// the allowlist. Origin is preferred (always set on cross-origin POSTs in
// modern browsers); Referer is the fallback because some legacy clients
// strip Origin on same-origin POSTs.
//
// Returns "" when neither header is present. Callers decide whether the
// absence is acceptable (it is for non-browser clients like the CLI,
// curl, the sidecar IPC) — see EnforceOrigin.
func requestOrigin(r *http.Request) string {
	if o := strings.TrimSpace(r.Header.Get("Origin")); o != "" && o != "null" {
		return strings.TrimRight(o, "/")
	}
	if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
		u, err := url.Parse(ref)
		if err == nil && u.Scheme != "" && u.Host != "" {
			return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
		}
	}
	return ""
}

// expectedSelfOrigin reconstructs scheme://host[:port] from r.Host, used
// when CREWSHIP_ALLOWED_ORIGINS is unset and we want "same-origin only".
// Scheme prefers TLS evidence (TLS handshake on the request, or
// X-Forwarded-Proto from a trusted proxy — extractIP's trust-proxy logic
// applies here too).
func expectedSelfOrigin(r *http.Request) string {
	host := r.Host
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if isTrustedProxy(remoteHopIP(r)) {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
			scheme = "https"
		}
	}
	return scheme + "://" + host
}

// EnforceOrigin returns middleware that rejects state-changing requests
// whose Origin (or Referer fallback) doesn't match either an allowlisted
// origin or — when no allowlist is configured — the server's own host.
//
// This is the backend half of the F-006 defense. Browsers already
// withhold the SameSite=Lax cookie on cross-site POSTs, so the practical
// CSRF risk pre-fix was small. The defense-in-depth gain is that any
// future cookie-policy regression, or any leak that lets an attacker
// send the Authorization: Bearer form (cookie value is also accepted
// there today), still fails on the backend.
//
// Non-browser clients (CLI tokens, curl scripts, the sidecar) typically
// don't send Origin/Referer. We allow that — gating those on Origin
// would break the CLI. The combo of "no Origin AND no cookie" is fine
// (anonymous), and "no Origin AND cookie" is the mobile/CLI-with-cookie
// case which we still allow because requiring Origin from those clients
// is impractical. Browsers always send Origin on cross-origin
// state-changing requests, so the realistic CSRF surface is covered.
func EnforceOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !methodIsStateChanging(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		origin := requestOrigin(r)
		if origin == "" {
			// No browser context — likely a CLI or server-to-server.
			// SameSite already handles browser CSRF; we don't have
			// signal to reject here without false positives.
			next.ServeHTTP(w, r)
			return
		}
		if originAllowed(origin, expectedSelfOrigin(r)) {
			next.ServeHTTP(w, r)
			return
		}
		// 403 with a stable, non-leaky reason. The header form lets the
		// frontend's apiFetch wrapper surface "your request was rejected
		// — your tab may be stale, please reload" rather than a generic
		// network error.
		w.Header().Set("WWW-Authenticate", `Bearer error="origin_rejected"`)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "request origin not allowed",
		})
	})
}

func originAllowed(got, self string) bool {
	if got == "" {
		return false
	}
	if self != "" && strings.EqualFold(got, self) {
		return true
	}
	for _, allowed := range allowedOriginSuffixes {
		if strings.EqualFold(got, allowed) {
			return true
		}
	}
	return false
}
