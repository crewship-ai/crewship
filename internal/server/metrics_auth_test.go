package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetricsAuthorized_LoopbackPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	assert.True(t, metricsAuthorized(req))
}

func TestMetricsAuthorized_LoopbackV6Peer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "[::1]:12345"
	assert.True(t, metricsAuthorized(req))
}

func TestMetricsAuthorized_PublicPeerNoToken(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	assert.False(t, metricsAuthorized(req),
		"public peer with no configured token must be denied — closes F-003")
}

func TestMetricsAuthorized_PublicPeerWrongToken(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "expected-secret")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	req.Header.Set("Authorization", "Bearer wrong-secret")
	assert.False(t, metricsAuthorized(req))
}

func TestMetricsAuthorized_PublicPeerCorrectToken(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "expected-secret")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	req.Header.Set("Authorization", "Bearer expected-secret")
	assert.True(t, metricsAuthorized(req))
}

func TestMetricsAuthorized_PublicPeerMissingScheme(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "expected-secret")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	// No "Bearer " prefix — invalid form must be rejected.
	req.Header.Set("Authorization", "expected-secret")
	assert.False(t, metricsAuthorized(req))
}

func TestIsLoopbackPeer(t *testing.T) {
	assert.True(t, isLoopbackPeer("127.0.0.1:1"))
	assert.True(t, isLoopbackPeer("127.0.0.5:65535"))
	assert.True(t, isLoopbackPeer("[::1]:443"))
	assert.False(t, isLoopbackPeer("192.168.1.1:80"))
	assert.False(t, isLoopbackPeer("8.8.8.8:443"))
	assert.False(t, isLoopbackPeer(""))
}

// TestMetricsAuthorized_ProxyHopWithPublicXFF_NoToken closes the dev2
// audit finding (gh #553): when a same-host reverse proxy (Caddy /
// nginx) fronts crewshipd, r.RemoteAddr is always loopback regardless
// of where the real request originated. The previous loopback bypass
// therefore exempted every public request that crossed the proxy.
//
// With CREWSHIP_METRICS_TOKEN unset, a request whose immediate hop is
// loopback (the proxy) but whose XFF points to a public client must
// be denied — the bypass should only fire when the true client IP is
// itself loopback.
func TestMetricsAuthorized_ProxyHopWithPublicXFF_NoToken(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:54321" // Caddy on the same host
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	assert.False(t, metricsAuthorized(req),
		"public client behind a trusted proxy must not inherit the proxy's loopback bypass — gh#553")
}

// TestMetricsAuthorized_ProxyHopWithPublicXFF_WrongToken hardens the
// same scenario when a token IS configured: the proxy hop must not
// short-circuit the token check.
func TestMetricsAuthorized_ProxyHopWithPublicXFF_WrongToken(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "expected-secret")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.Header.Set("Authorization", "Bearer wrong-secret")
	assert.False(t, metricsAuthorized(req))
}

// TestMetricsAuthorized_ProxyHopWithLoopbackXFF preserves the
// legitimate same-host scrape use case: when the true client (per XFF)
// is itself loopback — e.g. a sidecar Prometheus scraping through the
// proxy on 127.0.0.1 — the bypass should still fire.
func TestMetricsAuthorized_ProxyHopWithLoopbackXFF(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	assert.True(t, metricsAuthorized(req))
}

// TestMetricsAuthorized_UntrustedHopXFFIgnored: when the immediate hop
// is NOT a trusted proxy (i.e. a public peer), an attacker-supplied
// X-Forwarded-For: 127.0.0.1 must not grant the bypass.
func TestMetricsAuthorized_UntrustedHopXFFIgnored(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.50:12345" // public peer
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	assert.False(t, metricsAuthorized(req),
		"untrusted clients can spoof XFF; trust only when the immediate hop is itself a trusted proxy")
}

// TestMetrics_MethodMismatchIs404_NotEnumerable is the /metrics half of #1501.
//
// handleMetrics answers 404 rather than 401 for an unauthorized scrape,
// deliberately, so a scanner can't confirm the endpoint exists. That intent was
// undone one layer up: the route was registered as `GET /metrics`, so ServeMux
// rejected the METHOD before the handler ran and a bare `POST /metrics`
// returned 405 with `Allow: GET, HEAD` — confirming exactly what the 404 was
// hiding. An unauthorized caller must get the same answer whatever method it
// tries, byte for byte.
func TestMetrics_MethodMismatchIs404_NotEnumerable(t *testing.T) {
	t.Setenv("CREWSHIP_METRICS_TOKEN", "")
	s := newTestServer()

	// The reference: an unauthorized GET, i.e. the response the deliberate
	// 404 already produced.
	ref := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	ref.RemoteAddr = "203.0.113.50:55555" // public peer
	refRec := httptest.NewRecorder()
	s.mux.ServeHTTP(refRec, ref)
	if refRec.Code != http.StatusNotFound {
		t.Fatalf("unauthorized GET /metrics: got %d, want 404", refRec.Code)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/metrics", nil)
			req.RemoteAddr = "203.0.113.50:55555"
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code,
				"405 tells an unauthorized scanner the endpoint is real")
			assert.Empty(t, w.Header().Get("Allow"),
				"the Allow header enumerates the route just as loudly as the 405")
			assert.Equal(t, refRec.Body.String(), w.Body.String(),
				"body must be indistinguishable from the unauthorized GET")
			assert.Equal(t, refRec.Header().Get("Content-Type"), w.Header().Get("Content-Type"))
		})
	}
}

// TestMetrics_AuthorizedCallerStillGets405OnWrongMethod — hiding the endpoint
// from strangers must not cost an authorized scraper an honest error. Once the
// caller has proven it may see /metrics at all, a wrong method is a wrong
// method.
func TestMetrics_AuthorizedCallerStillGets405OnWrongMethod(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:55555" // authorized (loopback)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "GET, HEAD", w.Header().Get("Allow"))
}

// TestMetrics_AuthorizedGETStillServes guards the obvious regression: the
// method gate must not swallow the real scrape.
func TestMetrics_AuthorizedGETStillServes(t *testing.T) {
	s := newTestServer()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "/metrics", nil)
		req.RemoteAddr = "127.0.0.1:55555"
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, method)
	}
}
