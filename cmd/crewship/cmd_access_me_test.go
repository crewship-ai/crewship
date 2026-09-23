package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestAccessMeCommands(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/pipelines/a b/access/me", clitest.JSONResponse(200, map[string]any{
		"routine": "a b", "actions": map[string]any{"run": map[string]any{"state": "conditional", "reason": "runtime_preflight_required"}},
	}))
	cmd := accessMeCommand("routine")
	cmd.SetArgs([]string{"a b"})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil || !strings.Contains(out, "conditional") || !strings.Contains(out, "runtime_preflight_required") {
		t.Fatalf("routine access: %q, %v", out, err)
	}

	stub.OnGet("/api/v1/credentials/cred-1/access/me", clitest.JSONResponse(200, map[string]any{
		"credential_id": "cred-1", "actions": map[string]any{"reveal": map[string]any{"state": "denied", "reason": "non_interactive_auth"}},
	}))
	cmd = accessMeCommand("credential")
	cmd.SetArgs([]string{"cred-1"})
	out, err = captureStdout(t, cmd.Execute)
	if err != nil || !strings.Contains(out, "non_interactive_auth") {
		t.Fatalf("credential access: %q, %v", out, err)
	}
	if calls := stub.CallsFor("GET", "/api/v1/credentials/cred-1/access/me"); len(calls) != 1 || !strings.Contains(calls[0].Query, "workspace_id="+covWSCli3) {
		t.Fatalf("credential workspace query: %+v", calls)
	}

	stub.OnGet("/api/v1/credentials/hidden/access/me", clitest.ErrorResponse(404, "not found"))
	cmd = accessMeCommand("credential")
	cmd.SetArgs([]string{"hidden"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("hidden credential refusal swallowed")
	}
}
