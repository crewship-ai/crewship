package sidecar

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestProxy_FreeMode_AllowsAnyDomain(t *testing.T) {
	// Local target server so we don't depend on external DNS/network.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	targetHost := target.Listener.Addr().String()

	proxy := NewProxy(ProxyConfig{
		CredStore: NewCredStore(),
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Logger:    testLogger(),
		FreeMode:  true,
	})
	// Route "evil.com" requests to our local test server.
	proxy.transport = &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("tcp", targetHost)
		},
	}

	req := httptest.NewRequest(http.MethodGet, "http://evil.com/exfil", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Errorf("freeMode should not block domains, got 403")
	}
}

func TestProxy_RestrictedMode_BlocksUnknownDomains(t *testing.T) {
	proxy := NewProxy(ProxyConfig{
		CredStore: NewCredStore(),
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Logger:    testLogger(),
		FreeMode:  false,
	})

	req := httptest.NewRequest(http.MethodGet, "http://evil.com/exfil", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("restricted mode should block non-allowed domains, got %d", rec.Code)
	}
}

func TestServer_NetworkPolicy_Restricted_WithExtraDomains(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		NetworkPolicy: &NetworkPolicyConfig{
			Mode:           "restricted",
			AllowedDomains: []string{"github.com", "api.github.com"},
		},
		Logger: testLogger(),
	})

	al := srv.Allowlist()
	if !al.IsAllowed("api.anthropic.com") {
		t.Error("default domain api.anthropic.com should be allowed")
	}
	if !al.IsAllowed("github.com") {
		t.Error("extra domain github.com should be allowed")
	}
	if !al.IsAllowed("api.github.com") {
		t.Error("extra domain api.github.com should be allowed")
	}
	if al.IsAllowed("evil.com") {
		t.Error("evil.com should NOT be allowed")
	}
}

func TestServer_NetworkPolicy_Free_AllowsEverything(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: testLogger(),
	})

	// Send a request directly through the proxy's ServeHTTP to a domain
	// NOT on the allowlist. In free mode the proxy must NOT return 403.
	// Using httptest directly avoids DNS/network dependencies.
	req := httptest.NewRequest(http.MethodGet, "http://not-on-allowlist.test/test", nil)
	rec := httptest.NewRecorder()
	srv.proxy.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Error("free mode server should not return 403 for any domain")
	}
}

func TestServer_NetworkPolicy_Nil_DefaultsFree(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		NetworkPolicy: nil,
		Logger:        testLogger(),
	})

	if !srv.proxy.freeMode {
		t.Error("nil NetworkPolicy should default to freeMode=true")
	}
}

func TestServer_NetworkPolicy_Restricted(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		NetworkPolicy: &NetworkPolicyConfig{
			Mode: "restricted",
		},
		Logger: testLogger(),
	})

	if srv.proxy.freeMode {
		t.Error("restricted NetworkPolicy should set freeMode=false")
	}
}

func TestServer_NetworkPolicy_UnknownMode_FailsClosed(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		NetworkPolicy: &NetworkPolicyConfig{
			Mode: "yolo",
		},
		Logger: testLogger(),
	})

	if srv.proxy.freeMode {
		t.Error("unknown NetworkPolicy mode should default to restricted (freeMode=false)")
	}
}

func TestServer_NetworkPolicy_Health_ReportsMode(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		NetworkPolicy: &NetworkPolicyConfig{
			Mode: "restricted",
		},
		Logger: testLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Start(ctx)
	<-srv.Ready()

	resp, err := http.Get("http://" + srv.httpServer.Addr + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	var health struct {
		Status      string `json:"status"`
		NetworkMode string `json:"network_mode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.NetworkMode != "restricted" {
		t.Errorf("expected network_mode 'restricted', got %q", health.NetworkMode)
	}
}
