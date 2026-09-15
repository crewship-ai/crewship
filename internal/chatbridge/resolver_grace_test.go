package chatbridge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The middle link of #1882's grace hop, pinned on the decoding side:
// api.mcpCredEntry.GraceToken → JSON `grace_token` → credentialResponse →
// orchestrator.Credential.GraceToken. Dropped here, the sidecar boots with
// nothing to replay a 401 with and the grace overlap silently does not exist.
// Same shape as TestResolve_CarriesAgentIDsThroughToOrchestratorCredentials,
// for the same reason: only a real decode-and-map round trip sees a field that
// exists on one struct and never reaches the next.
func TestResolve_CarriesRotationGraceThroughToOrchestratorCredentials(t *testing.T) {
	const body = `{
		"agent_id": "agt_a",
		"agent_slug": "alpha",
		"credentials": [
			{
				"id": "ant-rotated",
				"env_var": "ANTHROPIC_API_KEY",
				"value": "sk-new",
				"type": "API_KEY",
				"provider": "ANTHROPIC",
				"grace_token": "sk-old",
				"grace_expires_at": "2026-01-02T00:00:00Z",
				"grace_rotation_id": "rot_1"
			},
			{
				"id": "ant-plain",
				"env_var": "ANTHROPIC_API_KEY_2",
				"value": "sk-plain",
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
	if len(info.Credentials) != 2 {
		t.Fatalf("resolved %d credentials, want 2", len(info.Credentials))
	}
	rotated, plain := info.Credentials[0], info.Credentials[1]
	if rotated.GraceToken != "sk-old" || rotated.GraceExpiresAt != "2026-01-02T00:00:00Z" || rotated.GraceRotationID != "rot_1" {
		t.Errorf("grace dropped between the API and the orchestrator: %+v", rotated)
	}
	if plain.GraceToken != "" || plain.GraceExpiresAt != "" || plain.GraceRotationID != "" {
		t.Errorf("grace invented for a credential the API sent none for: %+v", plain)
	}
}
