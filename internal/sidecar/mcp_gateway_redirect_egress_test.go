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
// credential headers — to a non-allowlisted host.
//
// The client is now the shared egresspolicy.Client (built in
// SetEgressAllowlist), whose CheckRedirect re-runs BOTH the crew allowlist and
// httpsafe.ValidateURL on each hop, so this test asserts the shared client's
// behaviour with public hosts (a literal private IP would be refused by the
// SSRF re-check regardless of the allowlist — which is the correct, stronger
// posture).
func TestMCPGatewayRedirectReGatesAllowlist(t *testing.T) {
	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "email", Transport: "streamable-http", Endpoint: "https://api.partner.com/x"},
	}, nil, newTestLogger())
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"api.partner.com"}), false)

	client := gw.clients["email"].httpClient
	if client.CheckRedirect == nil {
		t.Fatal("MCP httpClient must set CheckRedirect to re-gate the crew allowlist on every hop")
	}

	// A redirect to a non-allowlisted host is refused.
	blocked := httptest.NewRequest("POST", "https://blocked.invalid/collect", nil)
	if err := client.CheckRedirect(blocked, nil); err == nil {
		t.Error("redirect to a non-allowlisted host must be refused")
	}

	// A redirect that stays on the allowlisted host is followed.
	allowed := httptest.NewRequest("POST", "https://api.partner.com/next", nil)
	if err := client.CheckRedirect(allowed, nil); err != nil {
		t.Errorf("redirect to the allowlisted host must be followed, got %v", err)
	}
}

// Free mode / no allowlist must not block redirects (parity with endpointAllowed).
func TestMCPGatewayRedirectUngatedInFreeMode(t *testing.T) {
	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "email", Transport: "streamable-http", Endpoint: "https://api.partner.com/x"},
	}, nil, newTestLogger())
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"api.partner.com"}), true) // free mode

	client := gw.clients["email"].httpClient
	anyHost := httptest.NewRequest("POST", "https://anything.example/collect", nil)
	if err := client.CheckRedirect(anyHost, nil); err != nil {
		t.Errorf("free mode must not gate redirects, got %v", err)
	}
}
