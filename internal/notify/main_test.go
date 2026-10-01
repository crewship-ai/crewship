package notify

import (
	"net/http"
	"os"
	"testing"
)

// TestMain swaps the SSRF-safe webhook transport for the default transport so
// the package's httptest servers (which bind 127.0.0.1 — blocked by
// SafeTransport) are reachable in tests. Production never reassigns
// webhookTransport; the crew-egress block-proof tests assert the allowlist
// layer, which sits ABOVE the transport and is unaffected by this swap.
func TestMain(m *testing.M) {
	restore := SetWebhookTransportForTesting(http.DefaultTransport)
	code := m.Run()
	restore()
	os.Exit(code)
}
