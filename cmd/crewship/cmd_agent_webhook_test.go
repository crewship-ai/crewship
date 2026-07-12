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
