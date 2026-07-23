package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestMCPGatewayCrewEgressBlockedBeforeBytesLeave is the block-proof for
// #1367: an MCP server whose endpoint host is not on a RESTRICTED crew's
// allowlist is never connected — the initialize JSON-RPC (the first bytes
// out) never fires, and a tool call is refused with a crew-policy error.
// Before this fix the MCP gateway honored only the SSRF guard, so a
// domain-locked crew could reach any public MCP endpoint.
func TestMCPGatewayCrewEgressBlockedBeforeBytesLeave(t *testing.T) {
	var hits int32
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
	}))
	defer ep.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "email", Transport: "streamable-http", Endpoint: ep.URL},
	}, nil, newTestLogger())
	// Restricted, endpoint host (127.0.0.1) NOT on the allowlist.
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"api.partner.com"}), false)

	_ = gw.Connect(context.Background())
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("endpoint contacted %d time(s) during Connect; must block before bytes leave", n)
	}

	_, err := gw.CallTool(context.Background(), "email", "send", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "crew network policy") {
		t.Fatalf("CallTool err = %v, want crew network policy block", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("endpoint contacted %d time(s); MCP egress must block before bytes leave", n)
	}
}

// TestMCPGatewayCrewEgressAllowed confirms the gate does not over-block: a
// restricted crew that allowlists the endpoint host connects and calls tools.
func TestMCPGatewayCrewEgressAllowed(t *testing.T) {
	mcpSrv := mockMCPServer(t, []mcpToolDef{{Name: "send_email", Description: "x"}})
	defer mcpSrv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "email", Transport: "streamable-http", Endpoint: mcpSrv.URL},
	}, nil, newTestLogger())
	// Restricted, but the endpoint host (127.0.0.1) IS allowlisted → connects.
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"127.0.0.1"}), false)

	if err := gw.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp, err := gw.CallTool(context.Background(), "email", "send_email", []byte(`{"to":"x"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if resp.IsError {
		t.Errorf("unexpected tool error: %s", resp.Error)
	}
}

// TestMCPGatewayEgressUngatedByDefault documents the unit-path default: a
// gateway with no allowlist installed (nil) leaves MCP egress ungated, so the
// ~20 existing gateway unit tests that never call SetEgressAllowlist are
// unaffected. Production always installs the crew allowlist via NewServer.
func TestMCPGatewayEgressUngatedByDefault(t *testing.T) {
	gw := NewMCPGateway(nil, nil, newTestLogger())
	if ok, _ := gw.endpointAllowed("https://anything.example.com"); !ok {
		t.Error("nil allowlist must leave egress ungated")
	}
	// Free mode also skips the check.
	gw.SetEgressAllowlist(NewDomainAllowlist([]string{"api.partner.com"}), true)
	if ok, _ := gw.endpointAllowed("https://anything.example.com"); !ok {
		t.Error("free mode must skip the egress check")
	}
}
