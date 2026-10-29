package editororigin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"
)

// What a browser can actually send under /_editor/b/: Chromium resolves
// %2e%2e and .%2e segments itself (WHATWG URL parsing) before the request
// leaves the browser, so a front that decodes %2e and removes dot segments
// (RFC 3986 section 6.2.2) never sees a traversal it could route out of the
// prefix. Only ..%2f and ..%5c arrive unchanged. The check must therefore
// fail exactly the fronts that let a browser-sent URL under the prefix reach
// Crewship outside the editor, and pass the fronts that refuse or normalise
// harmlessly, however their refusals look.

// rfc3986Front decodes percent-encoded unreserved characters (%2e) and
// removes dot segments, keeping %2F and %5C encoded (Envoy normalize_path,
// CDN "normalize URLs to origin").
func rfc3986Front(p string) string {
	p = strings.NewReplacer("%2e", ".", "%2E", ".").Replace(p)
	var out []string
	for _, s := range strings.Split(p, "/") {
		switch s {
		case ".":
		case "..":
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, s)
		}
	}
	return strings.Join(out, "/")
}

func classFront(mode string, gated http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esc := strings.ToLower(r.URL.EscapedPath())
		switch mode {
		case "rfc3986":
			p := rfc3986Front(r.URL.EscapedPath())
			r = r.Clone(r.Context())
			r.URL.RawPath = p
			r.URL.Path = strings.NewReplacer("%2f", "%2F").Replace(p)
		case "waf-xfo":
			// A WAF or hardened proxy that refuses traversal spellings in
			// front and adds X-Frame-Options to its own error pages.
			if strings.Contains(esc, "..") || strings.Contains(esc, "%2e") || strings.Contains(esc, "%2f") || strings.Contains(esc, "%5c") {
				w.Header().Set("X-Frame-Options", "SAMEORIGIN")
				w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
				http.Error(w, "blocked", http.StatusForbidden)
				return
			}
		case "waf-plain":
			if strings.Contains(esc, "..") || strings.Contains(esc, "%2e") {
				http.Error(w, "blocked", http.StatusForbidden)
				return
			}
		case "sso-internal":
			if strings.HasPrefix(r.URL.Path, "/_editor/") {
				http.Redirect(w, r, "https://sso.corp.example:8443/oauth2/start?rd=https%3A%2F%2F10.20.30.40%2F_editor", http.StatusFound)
				return
			}
		case "nginx-uri":
			// proxy_pass with a URI: decodes everything and resolves the path.
			dec := strings.NewReplacer("%2f", "/", "%2F", "/", "%2e", ".", "%2E", ".", "%5c", "/", "%5C", "/").Replace(r.URL.EscapedPath())
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = path.Clean(dec), ""
		}
		gated.ServeHTTP(w, r)
	})
}

// productionStudio answers like the real Studio: every response carries
// X-Frame-Options DENY and frame-ancestors 'none'.
func productionStudio() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		switch r.URL.Path {
		case "/healthz", "/api/v1/auth/session":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	})
}

func TestCheckProxyClassifiesOnlyBrowserReachableEscapes(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		status    string
		isolation bool
	}{
		{"none", ProxyCheckPassed, false},
		{"rfc3986", ProxyCheckPassed, false},
		{"waf-plain", ProxyCheckPassed, false},
		{"waf-xfo", ProxyCheckPassed, false},
		{"nginx-uri", ProxyCheckFailed, true},
		{"sso-internal", ProxyCheckFailed, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h, _ := zeroConfigFixture(t)
			srv := httptest.NewServer(classFront(tc.mode, Gate(h, productionStudio())))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := CheckProxy(ctx, srv.Client(), srv.URL)
			if c.Status != tc.status || c.Isolation != tc.isolation {
				t.Fatalf("front %s: status %q isolation %v, want %q %v: %+v", tc.mode, c.Status, c.Isolation, tc.status, tc.isolation, c)
			}
		})
	}
}
