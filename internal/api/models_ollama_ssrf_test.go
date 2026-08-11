package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDefaultModelLister_OllamaRefusesHardBlockedEndpoint pins the SSRF floor
// on the model-discovery path.
//
// The judge endpoint is operator-set (POST /api/v1/admin/keeper/judge/*, gated
// at roleManage), and everywhere else it is dialled it goes through
// httpsafe.TrustedEndpointClient — a client that permits loopback and LAN,
// because an operator naming their own Ollama box is the normal case, while
// refusing the hard-blocked tier at dial time: cloud metadata endpoints and
// friends.
//
// This path did not. `defaultModelLister` built its OLLAMA lister with
// llm.NewOllama, which uses http.DefaultTransport, so the same admin-set URL
// that admin_keeper_judge.go refuses to dial at 169.254.169.254 was reachable
// here. Narrow — it needs an instance admin — but it is a real asymmetry, and
// "trusted enough to configure" is not the same as "trusted to reach the
// metadata service".
//
// 169.254.169.254 is the canonical link-local metadata address and is in
// httpsafe's hard-blocked tier, so it is blocked regardless of the
// allow-private opt-in.
func TestDefaultModelLister_OllamaRefusesHardBlockedEndpoint(t *testing.T) {
	lister, ok := defaultModelLister("OLLAMA", "", "http://169.254.169.254")
	if !ok {
		t.Fatal("expected an OLLAMA lister for a non-empty URL")
	}

	_, err := lister.ListModels(context.Background())
	if err == nil {
		t.Fatal("dialling a hard-blocked address succeeded — the discovery path is not behind the SSRF floor")
	}
	if !strings.Contains(err.Error(), "httpsafe:") {
		t.Errorf("want an httpsafe refusal, got: %v", err)
	}
}

// TestDefaultModelLister_OllamaStillReachesLoopback is the other half, and the
// reason this uses TrustedEndpointClient rather than the strict fence: an
// operator's own daemon on localhost or the LAN must keep working. Without
// this, "fix the SSRF" and "break every self-hosted Ollama" look identical.
//
// It dials a listener this test opens rather than a port assumed to be closed.
// The first version pointed at 127.0.0.1:1 and skipped if anything answered —
// a conditional skip on host state, which is the exact hazard the rest of this
// change removes, and the t.Skip ratchet was right to reject it.
func TestDefaultModelLister_OllamaStillReachesLoopback(t *testing.T) {
	// A real listener, so the dial demonstrably completes. What it serves does
	// not matter: the assertion is that httpsafe did not refuse the address.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	lister, ok := defaultModelLister("OLLAMA", "", srv.URL)
	if !ok {
		t.Fatal("expected an OLLAMA lister for a non-empty URL")
	}

	// 404 from the stub, so ListModels errors — but on the response, not on
	// the dial. Either outcome is fine here; an httpsafe refusal is not.
	_, err := lister.ListModels(context.Background())
	if err == nil {
		return
	}
	// Match on the "httpsafe:" prefix, not on one refusal's wording. The two
	// clients word it differently — TrustedEndpointClient says "blocked
	// address", SafeClient says "blocked outbound connection to
	// private/internal address" — and an assertion pinned to either would
	// have missed a swap to the strict fence, which refuses loopback and
	// would break every self-hosted Ollama.
	if strings.Contains(err.Error(), "httpsafe:") {
		t.Errorf("loopback must not be refused by the endpoint client: %v", err)
	}
}
