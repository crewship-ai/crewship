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
	// A stub daemon serving a real /api/tags payload, so this asserts the call
	// completes rather than merely that it was not refused. "Not blocked" and
	// "works" are different claims, and only the second one is what an
	// operator with Ollama on their laptop cares about.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5:7b"},{"name":"llama3:8b"}]}`))
	}))
	defer srv.Close()

	lister, ok := defaultModelLister("OLLAMA", "", srv.URL)
	if !ok {
		t.Fatal("expected an OLLAMA lister for a non-empty URL")
	}

	models, err := lister.ListModels(context.Background())
	if err != nil {
		// Distinguish the two failures explicitly: a refusal means the fence
		// is too strict for loopback, anything else means the stub is wrong.
		if strings.Contains(err.Error(), "httpsafe:") {
			t.Fatalf("loopback must not be refused by the endpoint client: %v", err)
		}
		t.Fatalf("ListModels against a loopback stub failed: %v", err)
	}

	if gotPath != "/api/tags" {
		t.Errorf("stub saw %q, want /api/tags", gotPath)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(models), models)
	}
	if models[0].ID != "qwen2.5:7b" {
		t.Errorf("first model = %q, want qwen2.5:7b", models[0].ID)
	}
}
