package editororigin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"
)

// Same-host isolation scopes CSP sources and Studio's frame-src to the
// bundle path. A browser drops the path of a source after a redirect, and a
// proxy that decodes and resolves dot segments can route a path under the
// prefix out of it. Nothing in Go does either; CheckProxy verifies that
// nothing in front does, through the public URL.

// front simulates what a customer's reverse proxy, CDN or SSO gate may do
// before a request reaches Crewship.
func front(mode string, gated http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "sso":
			// oauth2-proxy / access gateways redirect every unauthenticated
			// request; a credentialless frame is always unauthenticated.
			if strings.HasPrefix(r.URL.Path, "/_editor/") {
				http.Redirect(w, r, "/oauth2/start?rd="+url.QueryEscape(r.URL.Path), http.StatusFound)
				return
			}
		case "trailing-slash":
			if strings.HasSuffix(r.URL.Path, "/") && len(r.URL.Path) > 1 {
				http.Redirect(w, r, strings.TrimSuffix(r.URL.Path, "/"), http.StatusMovedPermanently)
				return
			}
		case "normalize":
			// nginx "proxy_pass http://upstream/;" decodes and resolves the path.
			if dec, err := url.PathUnescape(r.URL.EscapedPath()); err == nil {
				r = r.Clone(r.Context())
				r.URL.Path, r.URL.RawPath = path.Clean(strings.ReplaceAll(dec, "\\", "/")), ""
			}
		case "waf":
			// Rejecting odd encodings in front is fine: nothing reaches Studio.
			if strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%2") || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%5c") {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		case "unrouted":
			// The prefix is not routed to Crewship at all.
			if strings.HasPrefix(r.URL.Path, "/_editor/") {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("<h1>not found</h1>"))
				return
			}
		case "setcookie":
			w.Header().Add("Set-Cookie", "AWSALBCORS=abc; Path=/; SameSite=None; Secure")
		}
		gated.ServeHTTP(w, r)
	})
}

func proxyFixture(t *testing.T, mode string) (*httptest.Server, *Handler) {
	t.Helper()
	h, _ := sameHostFixture(t)
	studioApp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"user":"secret"}`))
	})
	srv := httptest.NewServer(front(mode, Gate(h, studioApp)))
	t.Cleanup(srv.Close)
	return srv, h
}

func TestCheckProxy(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		status    string
		isolation bool
		warning   string
	}{
		{"direct", ProxyCheckPassed, false, ""},
		{"waf", ProxyCheckPassed, false, ""},
		{"setcookie", ProxyCheckPassed, false, "cookie"},
		{"sso", ProxyCheckFailed, true, ""},
		{"trailing-slash", ProxyCheckFailed, true, ""},
		{"normalize", ProxyCheckFailed, true, ""},
		{"unrouted", ProxyCheckFailed, false, ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			srv, _ := proxyFixture(t, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got := CheckProxy(ctx, srv.Client(), srv.URL)
			if got.Status != tc.status || got.Isolation != tc.isolation {
				t.Fatalf("status %q isolation %v, want %q %v: %+v", got.Status, got.Isolation, tc.status, tc.isolation, got)
			}
			if (got.Status == ProxyCheckFailed) != (len(got.Problems) > 0) {
				t.Fatalf("problems do not match status: %+v", got)
			}
			if tc.warning != "" && !strings.Contains(strings.ToLower(strings.Join(got.Warnings, " ")), tc.warning) {
				t.Fatalf("missing %q warning: %+v", tc.warning, got)
			}
			if tc.warning == "" && len(got.Warnings) > 0 && tc.status == ProxyCheckPassed {
				t.Fatalf("unexpected warnings: %+v", got)
			}
		})
	}
	// A client that follows redirects must not hide them.
	srv, _ := proxyFixture(t, "trailing-slash")
	if got := CheckProxy(context.Background(), &http.Client{}, srv.URL); got.Status != ProxyCheckFailed || !got.Isolation {
		t.Fatalf("redirect hidden by the client's redirect policy: %+v", got)
	}
	// Nothing listening: no verdict, nothing to disable.
	srv.Close()
	if got := CheckProxy(context.Background(), &http.Client{Timeout: 2 * time.Second}, srv.URL); got.Status != ProxyCheckUnreachable || got.Isolation {
		t.Fatalf("unreachable origin: %+v", got)
	}
	if got := CheckProxy(context.Background(), http.DefaultClient, "not an origin"); got.Status != ProxyCheckUnreachable {
		t.Fatalf("invalid origin: %+v", got)
	}
}

// A failed check that defeats path scoping disables the editor: the prefix
// still belongs to the editor handler, but no bundle is served, with a
// message that names the cause. The application is unaffected.
func TestProxyCheckFailureDisablesSameHostEditor(t *testing.T) {
	h, digest := sameHostFixture(t)
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	gate := Gate(h, main)
	index := "/_editor/b/" + digest + "/index.html"
	if s := h.Status(); s.Mode != "same-host" || s.ProxyCheck != ProxyCheckPending || s.Disabled || s.URL != sameHostSelf {
		t.Fatalf("initial status %+v", s)
	}
	h.SetProxyCheck(ProxyCheck{Status: ProxyCheckFailed, Problems: []string{"x"}, Isolation: false})
	if w := serve(gate, "GET", "studio.example.test", index, frameNav...); w.Code != 200 {
		t.Fatalf("an unrouted prefix is not an isolation failure; status %d", w.Code)
	}
	h.SetProxyCheck(ProxyCheck{Status: ProxyCheckFailed, Problems: []string{"/_editor/b/x/ redirected (301)"}, Isolation: true})
	for _, p := range []string{index, "/_editor/b/" + digest + "/app.js"} {
		w := serve(gate, "GET", "studio.example.test", p, frameScript...)
		if w.Code != http.StatusServiceUnavailable || !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || !strings.Contains(w.Body.String(), "/_editor/") {
			t.Fatalf("%s while disabled: %d %q %v", p, w.Code, w.Body.String(), w.Header())
		}
	}
	if w := serve(gate, "GET", "studio.example.test", "/chat"); w.Code != 299 {
		t.Fatalf("application affected: %d", w.Code)
	}
	if s := h.Status(); !s.Disabled || s.ProxyCheck != ProxyCheckFailed || !strings.Contains(s.Detail, "redirected") {
		t.Fatalf("status %+v", s)
	}
	// A later pass does not re-enable a disabled editor; that takes a restart
	// after the proxy is fixed.
	h.SetProxyCheck(ProxyCheck{Status: ProxyCheckPassed})
	if w := serve(gate, "GET", "studio.example.test", index, frameNav...); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("re-enabled without restart: %d", w.Code)
	}
	d, _ := fixture(t)
	if s := d.Status(); s.Mode != "dedicated" || s.URL != editor {
		t.Fatalf("dedicated status %+v", s)
	}
	var none *Handler
	if s := none.Status(); s.Mode != "" {
		t.Fatalf("nil status %+v", s)
	}
}
