package editororigin

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Zero-config: no Studio origin is configured after install. The editor's
// policy names the origin each request reached; nothing else is assumed.

func policyFor(origin string) string {
	self := origin + "/_editor/b/"
	return "default-src 'none'; script-src " + self + " 'wasm-unsafe-eval'; style-src " + self + " 'unsafe-inline'; img-src " + self + " data: blob:; font-src " + self + "; connect-src " + self + "; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " + origin + "; webrtc 'block'; sandbox allow-scripts"
}

func TestZeroConfigPolicyFollowsTheRequest(t *testing.T) {
	h, digest := zeroConfigFixture(t)
	asset := "/_editor/b/" + digest + "/app.js"
	for _, tc := range []struct {
		host, origin string
		tls          bool
	}{
		// Without TLS evidence the scheme is http, which a CSP source also
		// matches for https: a proxy Crewship does not trust may end TLS.
		{"crewship.example.test", "http://crewship.example.test", false},
		{"crewship.example.test", "https://crewship.example.test", true},
		{"CREWSHIP.Example.test:443", "https://crewship.example.test", true},
		{"studio.other.example:8443", "https://studio.other.example:8443", true},
		{"10.0.0.5:8080", "http://10.0.0.5:8080", false},
		{"localhost:3000", "http://localhost:3000", false},
	} {
		r := httptest.NewRequest("GET", asset, nil)
		r.Host = tc.host
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		for i := 0; i+1 < len(frameScript); i += 2 {
			r.Header.Set(frameScript[i], frameScript[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Content-Security-Policy") != policyFor(tc.origin) {
			t.Errorf("%s: %d %q", tc.host, w.Code, w.Header().Get("Content-Security-Policy"))
		}
		if got := h.FrameSourceFor(r); got != tc.origin+"/_editor/b/" {
			t.Errorf("%s: frame source %q", tc.host, got)
		}
	}
	// The trusted-proxy rule decides the scheme when one is passed.
	hs, _ := sameHostWith(t, Options{Scheme: func(r *http.Request) string { return r.Header.Get("X-Test-Proto") }})
	r := httptest.NewRequest("GET", "/chat", nil)
	r.Host = "crewship.example.test"
	r.Header.Set("X-Test-Proto", "https")
	if got := hs.FrameSourceFor(r); got != "https://crewship.example.test/_editor/b/" {
		t.Fatalf("scheme rule ignored: %q", got)
	}
	r.Header.Set("X-Test-Proto", "javascript")
	if got := hs.FrameSourceFor(r); got != "http://crewship.example.test/_editor/b/" {
		t.Fatalf("unknown scheme passed through: %q", got)
	}
}

// A Host header no browser sends, or one a CSP source cannot name, gets no
// policy and no frame source.
func TestZeroConfigRefusesInvalidHosts(t *testing.T) {
	h, digest := zeroConfigFixture(t)
	for _, host := range []string{"", " ", "a b.test", "evil.test@studio.test", "studio.test/x", "studio.test;x", "[::1]:8080", "[::1]", "studio.test:", "studio.test:0", "studio.test:99999", "studio.test:8o", "-bad.test", "bad-.test", "a..b", ".", "*.example.test", "studio.test:80:80", "stüdio.test", strings.Repeat("a", 64) + ".test"} {
		r := httptest.NewRequest("GET", "/_editor/b/"+digest+"/index.html", nil)
		r.Host = host
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		w := httptest.NewRecorder()
		Gate(h, http.NotFoundHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || w.Header().Get("Content-Security-Policy") != sameHostDenyCSP {
			t.Errorf("host %q: %d %q", host, w.Code, w.Header().Get("Content-Security-Policy"))
		}
		if h.FrameSourceFor(r) != "" {
			t.Errorf("host %q got a frame source", host)
		}
	}
	for _, host := range []string{"studio.test.", "a-b.example.test:65535", "127.0.0.1:1"} {
		r := httptest.NewRequest("GET", "/chat", nil)
		r.Host = host
		if h.FrameSourceFor(r) == "" {
			t.Errorf("valid host %q refused", host)
		}
	}
}

// The Page runtime may share the Crewship process but never the editor:
// Studio would list that whole origin in frame-src.
func TestZeroConfigRefusesThePageRuntimeHost(t *testing.T) {
	h, digest := sameHostWith(t, Options{PageRuntimeOrigin: "https://apps.example.net"})
	for _, host := range []string{"apps.example.net", "APPS.example.net:443"} {
		w := serve(h, "GET", host, "/_editor/b/"+digest+"/index.html", frameNav...)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s: %d", host, w.Code)
		}
		r := httptest.NewRequest("GET", "/chat", nil)
		r.Host = host
		if h.FrameSourceFor(r) != "" {
			t.Errorf("%s: frame source on the Page runtime", host)
		}
	}
	if w := serve(h, "GET", "studio.example.net", "/_editor/b/"+digest+"/index.html", frameNav...); w.Code != 200 {
		t.Fatalf("Studio host refused: %d", w.Code)
	}
}

// Every same-host response says who answered, so CheckProxy can recognise
// a traversal that reached Crewship outside the editor.
func TestGateMarksWhoAnswered(t *testing.T) {
	h, digest := zeroConfigFixture(t)
	gate := Gate(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	for p, want := range map[string]string{
		"/_editor/b/" + digest + "/index.html": "editor",
		"/_editor/nope":                        "editor",
		"/healthz":                             "app",
		"/api/v1/auth/session":                 "app",
	} {
		if got := serve(gate, "GET", "crewship.example.test", p).Header().Get(RouteHeader); got != want {
			t.Errorf("%s: %s %q, want %q", p, RouteHeader, got, want)
		}
	}
	d, _ := fixture(t)
	if got := serve(Gate(d, http.NotFoundHandler()), "GET", "studio.example.test", "/").Header().Get(RouteHeader); got != "" {
		t.Fatalf("dedicated mode marks Studio responses: %q", got)
	}
}

// signedIn is a browser request from a Studio page, as the auth middleware
// hands it over after a session authenticated it.
func signedIn(host, referer string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
	r.Host = host
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Referer", referer)
	return r
}

func TestLearnOriginFromSignedInBrowserRequests(t *testing.T) {
	h, _ := zeroConfigFixture(t)
	if got := h.CheckTargets(); len(got) != 0 || h.Health() != HealthPending {
		t.Fatalf("targets before any browser request: %v %s", got, h.Health())
	}
	// Not browser same-origin requests: nothing learned.
	for _, r := range []*http.Request{
		func() *http.Request {
			r := signedIn("crewship.example.test", "https://crewship.example.test/")
			r.Header.Del("Sec-Fetch-Site")
			return r
		}(),
		func() *http.Request {
			r := signedIn("crewship.example.test", "https://crewship.example.test/")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		}(),
		func() *http.Request { r := signedIn("crewship.example.test", ""); return r }(),
		signedIn("crewship.example.test", "javascript:alert(1)"),
		signedIn("[::1]:8080", "http://[::1]:8080/"),
	} {
		learnAs(h, r, "u", true)
	}
	if got := h.CheckTargets(); len(got) != 0 {
		t.Fatalf("learned from a request that is not a signed-in browser page: %v", got)
	}
	select {
	case <-h.Wake():
		t.Fatal("woken without learning")
	default:
	}
	// The browser's own scheme and host, from Referer or Origin.
	learnAs(h, signedIn("crewship.example.test", "https://crewship.example.test/pages/budget?x=1"), "admin", true)
	learnAs(h, signedIn("crewship.example.test", "https://crewship.example.test/chat"), "admin", true)
	o := signedIn("other.example.test:8443", "")
	o.Header.Set("Origin", "https://other.example.test:8443")
	learnAs(h, o, "admin2", true)
	if got := h.CheckTargets(); strings.Join(got, ",") != "https://crewship.example.test,https://other.example.test:8443" {
		t.Fatalf("targets %v", got)
	}
	select {
	case <-h.Wake():
	default:
		t.Fatal("learning a new origin did not wake the self-check")
	}
	// A browser on one host while Crewship receives another: the proxy
	// rewrites Host and the editor cannot work there. Reported, not probed.
	learnAs(h, signedIn("127.0.0.1:8080", "https://rewritten.example.test/chat"), "admin3", true)
	if got := h.CheckTargets(); len(got) != 2 {
		t.Fatalf("host-rewritten origin probed: %v", got)
	}
	if h.Health() != HealthFailed {
		t.Fatalf("health with a rewritten Host: %s", h.Health())
	}
	var found bool
	for _, od := range h.Detail().Origins {
		if od.Origin == "https://rewritten.example.test" {
			found = od.Cause == CauseHostRewritten && od.ProxyCheck == ProxyCheckFailed && !od.Disabled && strings.Contains(strings.Join(od.Problems, " "), "proxy_set_header Host")
		}
	}
	if !found {
		t.Fatalf("rewritten Host not reported: %+v", h.Detail())
	}
	// The same browser origin later arrives with its Host: probe it.
	learnAs(h, signedIn("rewritten.example.test", "https://rewritten.example.test/chat"), "admin3", true)
	if got := h.CheckTargets(); len(got) != 3 {
		t.Fatalf("recovered origin not probed: %v", got)
	}
	// Bounded: a signed-in user cannot grow the list without limit.
	for _, host := range []string{"d.example.test", "e.example.test", "f.example.test"} {
		learnAs(h, signedIn(host, "https://"+host+"/"), host, false)
	}
	if got := h.CheckTargets(); len(got) != maxLearnedOrigins {
		t.Fatalf("learned %d origins: %v", len(got), got)
	}

	// A configured origin is the only target; nothing is learned.
	p, _ := sameHostFixture(t)
	learnAs(p, signedIn("crewship.example.test", "https://crewship.example.test/"), "admin", true)
	if got := p.CheckTargets(); len(got) != 1 || got[0] != studio {
		t.Fatalf("configured origin targets %v", got)
	}
	// The Page runtime is never a Studio origin.
	rt, _ := sameHostWith(t, Options{PageRuntimeOrigin: "https://apps.example.net"})
	learnAs(rt, signedIn("apps.example.net", "https://apps.example.net/"), "admin", true)
	if got := rt.CheckTargets(); len(got) != 0 {
		t.Fatalf("learned the Page runtime: %v", got)
	}
}
