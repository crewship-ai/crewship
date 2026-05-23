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
