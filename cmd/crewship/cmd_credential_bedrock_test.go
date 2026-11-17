package main

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCredentialBedrockAssignCLI(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{{"id": "account", "name": "Bedrock EU"}}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"id": "agent", "slug": "worker", "name": "Worker"}}))
	stub.OnPost("/api/v1/credentials/account/bedrock-assign", clitest.JSONResponse(200, map[string]any{"assigned": 1, "provider": "AWS_BEDROCK", "model": "amazon-bedrock/eu.anthropic.claude-opus-5-5"}))
	cmd := newCredentialBedrockAssignCmd()
	cmd.SetArgs([]string{"Bedrock EU", "--agent", "worker", "--model", "amazon-bedrock/eu.anthropic.claude-opus-5-5"})
	if _, err := captureStdout(t, cmd.Execute); err != nil {
		t.Fatal(err)
	}
	calls := stub.CallsFor("POST", "/api/v1/credentials/account/bedrock-assign")
	if len(calls) != 1 {
		t.Fatalf("requests=%d", len(calls))
	}
	var body struct {
		Model  string   `json:"model"`
		Agents []string `json:"agent_ids"`
	}
	if json.Unmarshal(calls[0].Body, &body) != nil || body.Model != "eu.anthropic.claude-opus-5-5" || len(body.Agents) != 1 || body.Agents[0] != "agent" {
		t.Fatalf("invalid assignment: %s", calls[0].Body)
	}
}
