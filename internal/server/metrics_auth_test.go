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
