package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestCredCreateCmd_Crews covers #1083 item 1: `credential create --crews`
// resolves crew slugs to IDs and sends them as crew_ids so the CLI can manage
// crew scoping at parity with the API/UI.
func TestCredCreateCmd_Crews(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew00000000000000000aa", "slug": "backend-team"},
	}))
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "gh-token"}))

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "gh-token", "type": "API_KEY", "provider": "NONE",
		"value": "secret-v", "crews": "backend-team",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/credentials")[0].Body, &body)
	crewIDs, ok := body["crew_ids"].([]any)
	if !ok || len(crewIDs) != 1 || crewIDs[0] != "ccrew00000000000000000aa" {
		t.Errorf("crew_ids not resolved+sent: %v", body["crew_ids"])
	}
}
