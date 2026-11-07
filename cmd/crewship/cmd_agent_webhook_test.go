package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestAgentRotateWebhookSecret pins the CLI parity for
// POST /api/v1/agents/{id}/webhook-secret/rotate (#999): the command
// resolves the slug, hits the rotate endpoint, and prints the minted
// secret exactly once (there is no read-back surface).
func TestAgentRotateWebhookSecret(t *testing.T) {
	t.Run("resolves slug and prints the new secret", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": "cagentviktor000000001", "slug": "viktor"},
		}))
		s.OnPost("/api/v1/agents/cagentviktor000000001/webhook-secret/rotate",
			clitest.JSONResponse(200, map[string]string{
				"webhook_secret": "whsec_new_value",
				"rotated_at":     "2026-07-12T00:00:00Z",
			}))
		covSetupCli10(t, s.URL())

		out, err := captureStdoutCovCli10(t, func() error {
			return agentRotateWebhookSecretCmd.RunE(agentRotateWebhookSecretCmd, []string{"viktor"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "whsec_new_value") {
			t.Errorf("stdout must print the minted secret once: %q", out)
		}
		if calls := s.CallsFor("POST", "/api/v1/agents/cagentviktor000000001/webhook-secret/rotate"); len(calls) != 1 {
			t.Errorf("rotate endpoint calls = %d, want 1", len(calls))
		}
	})

	t.Run("server error surfaced", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": "cagentviktor000000001", "slug": "viktor"},
		}))
		s.OnPost("/api/v1/agents/cagentviktor000000001/webhook-secret/rotate",
			clitest.ErrorResponse(403, "Forbidden"))
		covSetupCli10(t, s.URL())

		_, err := captureStdoutCovCli10(t, func() error {
			return agentRotateWebhookSecretCmd.RunE(agentRotateWebhookSecretCmd, []string{"viktor"})
		})
		if err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Fatalf("want 403 surfaced, got %v", err)
		}
	})
}

func TestSecretOverlapTTLParsing(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"none", 0, false}, {"0", 0, false}, {"60s", 60, false}, {"59s", 0, true},
		{"24h", 86400, false}, {"7d", 604800, false}, {"168h", 604800, false},
		{"7d1s", 0, true}, {"8d", 0, true}, {"", 0, true}, {"-1h", 0, true}, {"soon", 0, true},
		{"90m", 5400, false}, {"1500ms", 0, true},
	}
	for _, tc := range tests {
		got, err := parseSecretOverlapTTL(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parseSecretOverlapTTL(%q) = (%d, %v), want (%d, err=%v)", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// The overlap flag reaches the server as seconds, and the command reports
// when the old secret stops; revoke hits the dedicated DELETE endpoint.
func TestAgentRotateWebhookSecret_PreviousSecretTTLAndRevoke(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "cagentviktor000000001", "slug": "viktor"},
	}))
	s.OnPost("/api/v1/agents/cagentviktor000000001/webhook-secret/rotate",
		clitest.JSONResponse(200, map[string]string{
			"webhook_secret": "whsec_new_value", "rotated_at": "2026-07-12T00:00:00Z",
			"previous_secret_expires_at": "2026-07-13T00:00:00Z",
		}))
	s.OnDelete("/api/v1/agents/cagentviktor000000001/webhook-secret/previous",
		clitest.JSONResponse(200, map[string]bool{"revoked": true}))
	covSetupCli10(t, s.URL())

	if err := agentRotateWebhookSecretCmd.Flags().Set("previous-secret-ttl", "24h"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agentRotateWebhookSecretCmd.Flags().Set("previous-secret-ttl", "")
		agentRotateWebhookSecretCmd.Flags().Lookup("previous-secret-ttl").Changed = false
	})
	if _, err := captureStdoutCovCli10(t, func() error {
		return agentRotateWebhookSecretCmd.RunE(agentRotateWebhookSecretCmd, []string{"viktor"})
	}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/agents/cagentviktor000000001/webhook-secret/rotate")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"previous_secret_ttl_seconds":86400`) {
		t.Fatalf("rotate body = %v", calls)
	}

	if err := agentRotateWebhookSecretCmd.Flags().Set("previous-secret-ttl", "30s"); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdoutCovCli10(t, func() error {
		return agentRotateWebhookSecretCmd.RunE(agentRotateWebhookSecretCmd, []string{"viktor"})
	}); err == nil {
		t.Fatal("a TTL below 60s must be refused locally")
	}

	if _, err := captureStdoutCovCli10(t, func() error {
		return agentRevokePreviousWebhookSecretCmd.RunE(agentRevokePreviousWebhookSecretCmd, []string{"viktor"})
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/agents/cagentviktor000000001/webhook-secret/previous")); n != 1 {
		t.Fatalf("revoke calls = %d, want 1", n)
	}
}
