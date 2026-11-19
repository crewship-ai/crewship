package chatbridge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// #5542: the chat path is how ordinary runs resolve credentials. The
// ENDPOINT_URL proxy projection is admitted only with the revision of the
// exact Vault value, so dropping source_revision here makes every chat run
// through an authenticated endpoint fail with 403 at the host.
func TestResolve_CarriesEndpointSourceRevisionThroughToOrchestratorCredentials(t *testing.T) {
	const body = `{
		"agent_id": "agt_a",
		"agent_slug": "alpha",
		"credentials": [
			{"id": "ep", "env_var": "", "value": "synthetic", "type": "API_KEY", "provider": "OPENAI_COMPAT", "base_url": "https://fixture.example/v1", "source_revision": "rev-1"},
			{"id": "key", "env_var": "OPENAI_API_KEY", "value": "sk", "type": "API_KEY", "provider": "OPENAI"}
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
	if len(info.Credentials) != 2 || info.Credentials[0].SourceRevision != "rev-1" {
		t.Fatalf("source_revision dropped between the API and the orchestrator: %+v", info.Credentials)
	}
	if info.Credentials[1].SourceRevision != "" {
		t.Fatalf("source_revision invented: %+v", info.Credentials[1])
	}
}

// Every field the orchestrator credential carries over JSON must be decodable
// from the chat resolve response, or it silently disappears on the chat path
// while the other delivery paths keep it (as source_revision did).
func TestCredentialResponseCoversOrchestratorCredentialFields(t *testing.T) {
	tags := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				out[name] = true
			}
		}
		return out
	}
	have := tags(credentialResponse{})
	for name := range tags(orchestrator.Credential{}) {
		if !have[name] {
			t.Errorf("orchestrator.Credential json field %q is not decoded by the chat resolver", name)
		}
	}
}
