package main

// Provider logins at the terminal (PRD provider-logins §10.3, #2428):
// `credential create --type PROVIDER_LOGIN --mode … --from-file …`,
// `credential list --kind provider_login`, the login rows on `credential get`,
// `credential refresh`, and `agent get`'s "Pays with".

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const loginCredIDCli = "clogin000000000000000001"

func loginObject(status string) map[string]any {
	return map[string]any{
		"mode": "subscription", "provider": "OPENAI", "plan": "plus", "plan_label": "ChatGPT Plus",
		"owner_user_id": "u1", "owner_email": "jana@example.com", "expires_at": "2026-09-16T08:40:00Z",
		"refresh":  map[string]any{"supported": true, "status": status, "last_at": "2026-09-06T10:00:00Z", "next_at": "2026-09-15T08:40:00Z", "error": nil},
		"quota":    nil,
		"delivery": map[string]any{"kind": "file", "target": ".codex/auth.json"},
		"pays_for": map[string]any{"agents": 2, "crews": 1},
	}
}

func TestCredCreateCmd_ProviderLoginFromFile(t *testing.T) {
	stub := covStub(t)
	var got map[string]any
	stub.OnPost("/api/v1/credentials", func(r *http.Request, body []byte) (int, []byte, string) {
		_ = json.Unmarshal(body, &got)
		return clitest.JSONResponse(201, map[string]any{"id": loginCredIDCli, "name": "codex"})(r, body)
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"a","id_token":"i","refresh_token":"rt.x","account_id":"acct"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	covResetFlags(t, credCreateCmd)
	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "codex", "type": "provider_login", "provider": "openai", "mode": "subscription", "from-file": path,
	})
	if _, err := captureStdout(t, func() error { return credCreateCmd.RunE(credCreateCmd, nil) }); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got["type"] != "PROVIDER_LOGIN" || got["provider"] != "OPENAI" || got["mode"] != "subscription" {
		t.Errorf("body = %v", got)
	}
	// The file's trailing newline is not part of the login.
	if v, _ := got["value"].(string); !strings.HasPrefix(v, "{") || strings.HasSuffix(v, "\n") {
		t.Errorf("value = %q", v)
	}
}

func TestCredCreateCmd_ProviderLoginValidation(t *testing.T) {
	cases := []struct {
		name    string
		flags   map[string]string
		wantErr string
	}{
		{"mode without the type", map[string]string{"name": "x", "type": "API_KEY", "value": "v", "mode": "api_key"}, "--mode is only valid with --type PROVIDER_LOGIN"},
		{"bad mode", map[string]string{"name": "x", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "value": "v", "mode": "seat"}, "--mode must be"},
		{"needs a model provider", map[string]string{"name": "x", "type": "PROVIDER_LOGIN", "provider": "GITHUB", "value": "v"}, "needs --provider"},
		{"from-file with value", map[string]string{"name": "x", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "value": "v", "from-file": "/nonexistent"}, "cannot be combined"},
		{"from-file missing", map[string]string{"name": "x", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "from-file": "/nonexistent/auth.json"}, "read --from-file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covStub(t)
			covResetFlags(t, credCreateCmd)
			covSetFlags(t, credCreateCmd, tc.flags)
			err := credCreateCmd.RunE(credCreateCmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestCredListCmd_KindProviderLogin(t *testing.T) {
	stub := covStub(t)
	var gotQuery string
	stub.OnGet("/api/v1/credentials", func(r *http.Request, body []byte) (int, []byte, string) {
		gotQuery = r.URL.RawQuery
		return clitest.JSONResponse(200, map[string]any{"credentials": []map[string]any{
			{"id": loginCredIDCli, "name": "codex", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "status": "ACTIVE", "login": loginObject("ok")},
		}, "next_cursor": nil})(r, body)
	})
	covResetFlags(t, credListCmd)
	covSetFlags(t, credListCmd, map[string]string{"kind": "provider_login"})
	out, err := captureStdout(t, func() error { return credListCmd.RunE(credListCmd, nil) })
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(gotQuery, "kind=provider_login") {
		t.Errorf("query = %q, want kind=provider_login", gotQuery)
	}
	for _, want := range []string{"ChatGPT Plus", "jana@example.com", "subscription", "2026-09-16T08:40:00Z", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	covResetFlags(t, credListCmd)
	covSetFlags(t, credListCmd, map[string]string{"kind": "secret"})
	if err := credListCmd.RunE(credListCmd, nil); err == nil || !strings.Contains(err.Error(), "--kind must be provider_login") {
		t.Errorf("kind=secret err = %v", err)
	}
}

func TestCredGetCmd_ShowsLogin(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials/"+loginCredIDCli, clitest.JSONResponse(200, map[string]any{
		"id": loginCredIDCli, "name": "codex", "type": "PROVIDER_LOGIN", "provider": "OPENAI", "status": "ACTIVE", "scope": "WORKSPACE",
		"created_at": "2026-09-06T09:00:00Z", "login": loginObject("needs_relogin"),
	}))
	out, err := captureStdout(t, func() error { return credGetCmd.RunE(credGetCmd, []string{loginCredIDCli}) })
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, want := range []string{"Mode", "subscription", "ChatGPT Plus", "jana@example.com", "needs_relogin", "file .codex/auth.json", "2 agents, 1 crews"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCredRefreshCmd(t *testing.T) {
	stub := covStub(t)
	posted := false
	stub.OnPost("/api/v1/credentials/"+loginCredIDCli+"/refresh", func(r *http.Request, body []byte) (int, []byte, string) {
		posted = true
		return clitest.JSONResponse(200, map[string]any{"login": loginObject("ok")})(r, body)
	})
	out, err := captureStdout(t, func() error { return credRefreshCmd.RunE(credRefreshCmd, []string{loginCredIDCli}) })
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !posted || !strings.Contains(out, "ok") || !strings.Contains(out, "2026-09-15T08:40:00Z") {
		t.Errorf("posted=%v output:\n%s", posted, out)
	}

	// 409 while in flight surfaces as the server's error.
	stub2 := covStub(t)
	stub2.OnPost("/api/v1/credentials/"+loginCredIDCli+"/refresh", clitest.ErrorResponse(409, "a refresh of this login is already in flight"))
	if err := credRefreshCmd.RunE(credRefreshCmd, []string{loginCredIDCli}); err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Errorf("409 err = %v", err)
	}
}

func TestCredentialCmds_Refresh_Guards(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{{name: "refresh", cmd: credRefreshCmd, args: []string{loginCredIDCli}}})
	covRunNoWorkspace(t, []covCmdCase{{name: "refresh", cmd: credRefreshCmd, args: []string{loginCredIDCli}}})
	covRunTransportError(t, []covCmdCase{{name: "refresh", cmd: credRefreshCmd, args: []string{loginCredIDCli}}})
}

func TestAgentGetCmd_PaysWith(t *testing.T) {
	stub := covStub(t)
	agentID := "cagent00000000000000000a"
	stub.OnGet("/api/v1/agents/"+agentID, clitest.JSONResponse(200, map[string]any{
		"id": agentID, "name": "Reviewer", "slug": "reviewer", "agent_role": "WORKER", "status": "IDLE", "cli_adapter": "CODEX_CLI",
		"tool_profile": "CODING", "timeout_seconds": 600, "created_at": "2026-09-06T09:00:00Z",
		"_count":    map[string]any{"skills": 0, "credentials": 1, "chats": 0},
		"pays_with": map[string]any{"credential_id": loginCredIDCli, "name": "codex", "login": loginObject("ok")},
	}))
	out, err := captureStdout(t, func() error { return agentGetCmd.RunE(agentGetCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("agent get: %v", err)
	}
	for _, want := range []string{"Pays with", "codex (" + loginCredIDCli + ")", "ChatGPT Plus", "jana@example.com", "refresh ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
