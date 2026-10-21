package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestChatRunPreflightCLIUsesReadOnlyAuthorizedAPI(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/chats/c_1/run-preflight", clitest.JSONResponse(200, map[string]any{"estimate_state": "unknown", "estimate_usd": nil, "budgets": []any{}, "gate": map[string]any{"predicted_mode": "sync"}}))
	cmd := newChatRunPreflightCommand()
	out := covCaptureStdoutCli3(t, func() {
		if err := cmd.RunE(cmd, []string{"c_1"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "unknown") || !strings.Contains(out, "predicted_mode") {
		t.Fatal(out)
	}
	if len(stub.CallsFor("GET", "/api/v1/chats/c_1/run-preflight")) != 1 {
		t.Fatal("expected one authorized read")
	}
}
