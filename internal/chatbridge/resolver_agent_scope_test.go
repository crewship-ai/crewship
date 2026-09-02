package chatbridge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The middle link of #2052's ownership hop, pinned on the decoding side.
//
// api.mcpCredEntry.AgentIDs → JSON `agent_ids` → credentialResponse.AgentIDs →
// orchestrator.Credential.AgentIDs. Both halves fail OPEN when dropped: a
// missing tag decodes to nil and a missing assignment copies nil, nil means
// crew-wide, and the crew's sidecar goes back to serving one member's endpoint
// credential to another — with nothing failing anywhere. The API side of the
// same hop is pinned by TestAgentScope_BootResolverCarriesGrantedAgentIDs; the
// sidecar side by TestSidecarCredWireTags.
//
// Driven through the real resolve() against a canned response rather than by
// calling json.Unmarshal here, because the defect this guards is a field that
// exists on one struct and never reaches the next — which only a round trip
// through the actual decode-and-map can see.
func TestResolve_CarriesAgentIDsThroughToOrchestratorCredentials(t *testing.T) {
	const body = `{
		"agent_id": "agt_a",
		"agent_slug": "alpha",
		"credentials": [
			{
				"id": "compat-a",
				"env_var": "",
				"value": "sk-a",
				"type": "API_KEY",
				"provider": "OPENAI_COMPAT",
				"base_url": "https://a.example/v1",
				"agent_ids": ["agt_a"]
			},
			{
				"id": "ant-crew",
				"env_var": "ANTHROPIC_API_KEY",
				"value": "sk-ant",
				"type": "API_KEY",
				"provider": "ANTHROPIC"
			}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := NewIPCResolver(srv.URL, "test-internal-token", slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	info, err := r.ResolveAgent(context.Background(), "agt_a", "")
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}

	byID := map[string][]string{}
	for _, c := range info.Credentials {
		byID[c.ID] = c.AgentIDs
	}

	got, ok := byID["compat-a"]
	if !ok {
		t.Fatal("the scoped credential did not survive the resolve")
	}
	if strings.Join(got, ",") != "agt_a" {
		t.Errorf("AgentIDs = %v, want [agt_a]: ownership was dropped between the API and "+
			"the orchestrator, so the boot payload calls every credential crew-wide", got)
	}
	if crewWide, ok := byID["ant-crew"]; !ok {
		t.Error("the crew-wide credential did not survive the resolve")
	} else if crewWide != nil {
		t.Errorf("AgentIDs = %v, want nil: a credential with no agent_ids on the wire must "+
			"stay crew-wide", crewWide)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
