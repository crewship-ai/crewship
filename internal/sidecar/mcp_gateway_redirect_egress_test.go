package sidecar

import (
	"net/http/httptest"
	"testing"
)

// TestMCPGatewayRedirectReGatesAllowlist is the redirect-bypass proof for
// #1367 on the MCP path: the per-server JSON-RPC http.Client must re-check the
// crew allowlist on every redirect hop, not just the configured endpoint at
// Connect/CallTool. Before this fix the client had no CheckRedirect, so a
// malicious-but-allowlisted MCP server could 302 its POST — carrying injected
// credential headers — to a non-allowlisted host. (SafeTransport still blocks
// SSRF on each hop's dial; this closes the crew-allowlist half.)
func TestMCPGatewayRedirectReGatesAllowlist(t *testing.T) {
	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "email", Transport: "streamable-http", Endpoint: "http://127.0.0.1:1/x"},
	}, nil, newTestLogger())
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"127.0.0.1"}), false)

	client := gw.clients["email"].httpClient
	if client.CheckRedirect == nil {
		t.Fatal("MCP httpClient must set CheckRedirect to re-gate the crew allowlist on every hop")
	}

	// A redirect to a non-allowlisted host is refused.
	blocked := httptest.NewRequest("POST", "http://blocked.invalid/collect", nil)
	if err := client.CheckRedirect(blocked, nil); err == nil {
		t.Error("redirect to a non-allowlisted host must be refused")
	}

	// A redirect that stays on the allowlisted host is followed.
	allowed := httptest.NewRequest("POST", "http://127.0.0.1/next", nil)
	if err := client.CheckRedirect(allowed, nil); err != nil {
		t.Errorf("redirect to the allowlisted host must be followed, got %v", err)
	}
}

// Free mode / no allowlist must not block redirects (parity with endpointAllowed).
func TestMCPGatewayRedirectUngatedInFreeMode(t *testing.T) {
	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "email", Transport: "streamable-http", Endpoint: "http://127.0.0.1:1/x"},
	}, nil, newTestLogger())
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"api.partner.com"}), true) // free mode

	client := gw.clients["email"].httpClient
	anyHost := httptest.NewRequest("POST", "http://anything.example/collect", nil)
	if err := client.CheckRedirect(anyHost, nil); err != nil {
		t.Errorf("free mode must not gate redirects, got %v", err)
	}
}
