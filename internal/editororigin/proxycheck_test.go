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
		case "host-rewrite":
			// nginx proxy_pass without "proxy_set_header Host $host", Apache
			// without ProxyPreserveHost: Crewship sees the upstream name.
			r = r.Clone(r.Context())
			r.Host = "127.0.0.1:8080"
		case "strip-marker":
			// A proxy that removes unknown response headers.
			gated.ServeHTTP(stripHeader{w, RouteHeader}, r)
			return
		}
		gated.ServeHTTP(w, r)
	})
}

func proxyFixture(t *testing.T, mode string) (*httptest.Server, *Handler) {
	t.Helper()
	h, _ := zeroConfigFixture(t)
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
		{"host-rewrite", ProxyCheckFailed, false, ""},
		{"strip-marker", ProxyCheckFailed, true, ""},
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

type stripHeader struct {
	http.ResponseWriter
	name string
}

func (s stripHeader) WriteHeader(code int) {
	s.ResponseWriter.Header().Del(s.name)
	s.ResponseWriter.WriteHeader(code)
}

func (s stripHeader) Write(b []byte) (int, error) {
	s.ResponseWriter.Header().Del(s.name)
	return s.ResponseWriter.Write(b)
}

func TestCheckProxyCauses(t *testing.T) {
	for mode, cause := range map[string]string{
		"sso":          CauseRedirect,
		"normalize":    CausePathRewrite,
		"unrouted":     CauseNotRouted,
		"host-rewrite": CauseHostRewritten,
		"strip-marker": CauseNoMarker,
	} {
		srv, _ := proxyFixture(t, mode)
		if got := CheckProxy(context.Background(), srv.Client(), srv.URL); got.Cause != cause {
			t.Errorf("%s: cause %q, want %q: %+v", mode, got.Cause, cause, got)
		}
	}
	// A configured origin that the proxy does not forward as Host is
	// refused by the editor (421); the check names the cause.
	h, _ := sameHostFixture(t)
	srv := httptest.NewServer(Gate(h, http.NotFoundHandler()))
	defer srv.Close()
	if got := CheckProxy(context.Background(), srv.Client(), srv.URL); got.Status != ProxyCheckFailed || got.Isolation || got.Cause != CauseHostRewritten {
		t.Fatalf("configured origin seen under another Host: %+v", got)
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
	if s := h.Detail(); s.Mode != "same-host" || s.Status != HealthPending || len(s.Origins) != 1 || s.Origins[0].Origin != studio || s.Origins[0].ProxyCheck != ProxyCheckPending {
		t.Fatalf("initial detail %+v", s)
	}
	if got := h.CheckTargets(); len(got) != 1 || got[0] != studio {
		t.Fatalf("targets %v", got)
	}
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckFailed, Problems: []string{"x"}, Cause: CauseNotRouted})
	if w := serve(gate, "GET", "studio.example.test", index, frameNav...); w.Code != 200 {
		t.Fatalf("an unrouted prefix is not an isolation failure; status %d", w.Code)
	}
	if h.Health() != HealthFailed {
		t.Fatalf("health %s", h.Health())
	}
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckFailed, Problems: []string{"/_editor/b/x/ redirected (301)"}, Isolation: true, Cause: CauseRedirect})
	for _, p := range []string{index, "/_editor/b/" + digest + "/app.js"} {
		w := serve(gate, "GET", "studio.example.test", p, frameScript...)
		if w.Code != http.StatusServiceUnavailable || !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || !strings.Contains(w.Body.String(), "/_editor/") || strings.Contains(w.Body.String(), "redirected") {
			t.Fatalf("%s while disabled: %d %q %v", p, w.Code, w.Body.String(), w.Header())
		}
	}
	if w := serve(gate, "GET", "studio.example.test", "/chat"); w.Code != 299 {
		t.Fatalf("application affected: %d", w.Code)
	}
	if s := h.Detail(); s.Status != HealthDisabled || !s.Origins[0].Disabled || s.Origins[0].ProxyCheck != ProxyCheckFailed || !strings.Contains(strings.Join(s.Origins[0].Problems, " "), "redirected") {
		t.Fatalf("detail %+v", s)
	}
	// Unreachable decides nothing.
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckUnreachable})
	if s := h.Detail(); !s.Origins[0].Disabled || s.Origins[0].ProxyCheck != ProxyCheckFailed {
		t.Fatalf("unreachable changed the verdict: %+v", s)
	}
	d, _ := fixture(t)
	if s := d.Detail(); s.Mode != "dedicated" || s.Origin != editor || d.Health() != HealthOK {
		t.Fatalf("dedicated detail %+v", s)
	}
	var none *Handler
	if s := none.Detail(); s.Mode != "off" || none.Health() != "" {
		t.Fatalf("nil detail %+v", s)
	}
}

// One origin's failure disables the editor on that origin only: the check
// probes each public origin browsers use, and a request on another host is
// judged by that host's own check.
func TestDisablingIsPerOrigin(t *testing.T) {
	h, digest := zeroConfigFixture(t)
	index := "/_editor/b/" + digest + "/index.html"
	for _, host := range []string{"a.example.test", "b.example.test"} {
		learnAs(h, signedIn(host, "https://"+host+"/chat"), host, false)
	}
	h.RecordCheck("https://a.example.test", ProxyCheck{Status: ProxyCheckFailed, Isolation: true, Cause: CauseRedirect, Problems: []string{"x"}})
	if w := serve(h, "GET", "a.example.test", index, frameNav...); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled origin served: %d", w.Code)
	}
	if w := serve(h, "GET", "b.example.test", index, frameNav...); w.Code != http.StatusOK {
		t.Fatalf("other origin refused: %d", w.Code)
	}
}
