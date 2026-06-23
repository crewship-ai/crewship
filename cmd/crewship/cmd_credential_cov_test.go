package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covCredIDCli3 = "ccred00000000000000000aa"

// ─── resolveCredentialID ────────────────────────────────────────────────

func TestResolveCredentialID(t *testing.T) {
	t.Run("CUID passthrough", func(t *testing.T) {
		covStub(t) // ensures no accidental network use
		got, err := resolveCredentialID(newAPIClient(), covCredIDCli3)
		if err != nil {
			t.Fatalf("resolveCredentialID: %v", err)
		}
		if got != covCredIDCli3 {
			t.Errorf("got %q want %q", got, covCredIDCli3)
		}
	})

	t.Run("name lookup", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{
			{"id": "cid1", "name": "other"},
			{"id": covCredIDCli3, "name": "gh-token"},
		}))
		got, err := resolveCredentialID(newAPIClient(), "gh-token")
		if err != nil {
			t.Fatalf("resolveCredentialID: %v", err)
		}
		if got != covCredIDCli3 {
			t.Errorf("got %q want %q", got, covCredIDCli3)
		}
	})

	t.Run("not found", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{}))
		_, err := resolveCredentialID(newAPIClient(), "ghost")
		if err == nil || !strings.Contains(err.Error(), `credential "ghost" not found`) {
			t.Fatalf("expected not-found error, got %v", err)
		}
	})

	t.Run("list error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/credentials", clitest.ErrorResponse(500, "list broke"))
		_, err := resolveCredentialID(newAPIClient(), "ghost")
		if err == nil || !strings.Contains(err.Error(), "list broke") {
			t.Fatalf("expected list error, got %v", err)
		}
	})
}

// ─── testCredentialValue ────────────────────────────────────────────────

func TestTestCredentialValue(t *testing.T) {
	t.Run("skips SECRET type", func(t *testing.T) {
		covStub(t)
		valid, msg := testCredentialValue(newAPIClient(), "GITHUB", "SECRET", "v")
		if !valid || msg != "" {
			t.Errorf("SECRET should skip validation: valid=%v msg=%q", valid, msg)
		}
	})
	t.Run("skips NONE / empty provider", func(t *testing.T) {
		covStub(t)
		for _, p := range []string{"", "NONE"} {
			if valid, _ := testCredentialValue(newAPIClient(), p, "API_KEY", "v"); !valid {
				t.Errorf("provider %q should skip validation", p)
			}
		}
	})
	t.Run("skips OAuth tokens", func(t *testing.T) {
		covStub(t)
		valid, _ := testCredentialValue(newAPIClient(), "ANTHROPIC", "API_KEY", "sk-ant-oat-xyz")
		if !valid {
			t.Error("sk-ant-oat* should skip validation")
		}
	})
	t.Run("posts to test endpoint and decodes", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost("/api/v1/credentials/test",
			clitest.JSONResponse(200, map[string]any{"valid": false, "error": "401 from provider"}))
		valid, msg := testCredentialValue(newAPIClient(), "GITHUB", "API_KEY", "ghp_x")
		if valid || msg != "401 from provider" {
			t.Errorf("got valid=%v msg=%q", valid, msg)
		}
		calls := stub.CallsFor("POST", "/api/v1/credentials/test")
		if len(calls) != 1 {
			t.Fatalf("expected 1 test POST, got %d", len(calls))
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(calls[0].Body, &body)
		if body["provider"] != "GITHUB" || body["type"] != "API_KEY" || body["value"] != "ghp_x" {
			t.Errorf("test body wrong: %v", body)
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost("/api/v1/credentials/test", clitest.ErrorResponse(500, "validator down"))
		valid, msg := testCredentialValue(newAPIClient(), "GITHUB", "API_KEY", "v")
		if valid || !strings.Contains(msg, "test request failed") {
			t.Errorf("got valid=%v msg=%q", valid, msg)
		}
	})
	t.Run("malformed response body", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost("/api/v1/credentials/test", clitest.TextResponse(200, "not json"))
		valid, msg := testCredentialValue(newAPIClient(), "GITHUB", "API_KEY", "v")
		if valid || msg != "failed to read test result" {
			t.Errorf("got valid=%v msg=%q", valid, msg)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		covStub(t)
		cliCfg.Server = "http://127.0.0.1:1"
		valid, msg := testCredentialValue(newAPIClient(), "GITHUB", "API_KEY", "v")
		if valid || !strings.Contains(msg, "test request failed") {
			t.Errorf("got valid=%v msg=%q", valid, msg)
		}
	})
}

// ─── confirmInvalidKey (non-TTY paths) ──────────────────────────────────

func TestConfirmInvalidKey_NonTTY(t *testing.T) {
	// Test stdin is a pipe, never a TTY, so the plain-stdin fallback runs.
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"", false}, // EOF without input
	}
	for _, tc := range cases {
		t.Run("input "+strings.TrimSpace(tc.input), func(t *testing.T) {
			covWithStdin(t, tc.input)
			if got := confirmInvalidKey("provider said no"); got != tc.want {
				t.Errorf("confirmInvalidKey(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ─── credential list / get ──────────────────────────────────────────────

func TestCredListCmd(t *testing.T) {
	stub := covStub(t)
	system := "system"
	svc := "grafana"
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{
		{"id": "c1", "name": "user-key", "type": "API_KEY", "provider": "GITHUB", "status": "ACTIVE",
			"_count_agent_credentials": 2},
		{"id": "c2", "name": "auto-key", "type": "SECRET", "provider": "NONE", "status": "ACTIVE",
			"created_by_actor_type": system, "provisioned_for_service": svc},
	}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credListCmd.RunE(credListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "user-key") || !strings.Contains(out, "auto-key") {
		t.Errorf("rows missing: %q", out)
	}
	if !strings.Contains(out, "user") {
		t.Errorf("default 'user' source missing: %q", out)
	}
	if !strings.Contains(out, "system (grafana)") {
		t.Errorf("system+service source missing: %q", out)
	}
}

func TestCredListCmd_APIError(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials", clitest.ErrorResponse(500, "nope"))
	if err := credListCmd.RunE(credListCmd, nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestCredGetCmd(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]any{
		"id": covCredIDCli3, "name": "gh-token", "type": "API_KEY", "provider": "GITHUB",
		"status": "ACTIVE", "scope": "WORKSPACE", "created_at": "2026-06-01T00:00:00Z",
	}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credGetCmd.RunE(credGetCmd, []string{covCredIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"gh-token", "API_KEY", "GITHUB", "WORKSPACE"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail output missing %q: %q", want, out)
		}
	}
}

func TestCredGetCmd_NotFound(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.ErrorResponse(404, "credential not found"))
	err := credGetCmd.RunE(credGetCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "credential not found") {
		t.Fatalf("expected 404, got %v", err)
	}
}

// ─── rotations ──────────────────────────────────────────────────────────

func TestCredRotationsCmd(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3+"/rotations",
		clitest.JSONResponse(200, []map[string]any{
			{"id": "rot1", "credential_id": covCredIDCli3, "grace_seconds": 3600,
				"rotated_at": "2026-06-01T10:00:00Z", "expires_at": "2026-06-01T11:00:00Z",
				"rotated_by": "user1", "status": "ACTIVE", "old_value_gone": false},
		}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credRotationsCmd.RunE(credRotationsCmd, []string{covCredIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "rot1") || !strings.Contains(out, "3600") {
		t.Errorf("rotation row missing: %q", out)
	}
	if !strings.Contains(out, "2026-06-01 10:00:00") {
		t.Errorf("rotated_at not reformatted: %q", out)
	}
}

// ─── audit ──────────────────────────────────────────────────────────────

func TestCredAuditCmd(t *testing.T) {
	t.Run("limit validation", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, credAuditCmd)
		if err := credAuditCmd.Flags().Set("limit", "0"); err != nil {
			t.Fatal(err)
		}
		err := credAuditCmd.RunE(credAuditCmd, []string{covCredIDCli3})
		if err == nil || !strings.Contains(err.Error(), "--limit must be between 1 and 500") {
			t.Fatalf("expected limit error, got %v", err)
		}
	})

	t.Run("happy path with field fallbacks", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credAuditCmd)
		agent := "agent-1"
		ip := "10.0.0.1"
		stub.OnGet("/api/v1/credentials/"+covCredIDCli3+"/audit",
			clitest.JSONResponse(200, []map[string]any{
				{"id": "e1", "event_type": "USE", "agent_id": agent, "ip_address": ip,
					"occurred_at": "2026-06-01T10:00:00.123456Z"},
				{"id": "e2", "event_type": "ROTATE", "occurred_at": "2026-06-01T11:00:00Z"},
			}))

		out := covCaptureStdoutCli3(t, func() {
			if err := credAuditCmd.RunE(credAuditCmd, []string{covCredIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "USE") || !strings.Contains(out, "ROTATE") {
			t.Errorf("event rows missing: %q", out)
		}
		if !strings.Contains(out, "agent-1") || !strings.Contains(out, "10.0.0.1") {
			t.Errorf("agent/ip columns missing: %q", out)
		}
		calls := stub.CallsFor("GET", "/api/v1/credentials/"+covCredIDCli3+"/audit")
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=50") {
			t.Errorf("expected limit=50 default, got %+v", calls)
		}
	})
}

// ─── test-stored ────────────────────────────────────────────────────────

func TestCredTestStoredCmd(t *testing.T) {
	path := "/api/v1/credentials/" + covCredIDCli3 + "/test"

	t.Run("valid", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(path, clitest.JSONResponse(200, map[string]any{"valid": true}))
		if err := credTestStoredCmd.RunE(credTestStoredCmd, []string{covCredIDCli3}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	t.Run("invalid with message", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(path, clitest.JSONResponse(200, map[string]any{"valid": false, "error": "expired key"}))
		err := credTestStoredCmd.RunE(credTestStoredCmd, []string{covCredIDCli3})
		if err == nil || !strings.Contains(err.Error(), "expired key") {
			t.Fatalf("expected invalid error, got %v", err)
		}
	})

	t.Run("invalid without message", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(path, clitest.JSONResponse(200, map[string]any{"valid": false}))
		err := credTestStoredCmd.RunE(credTestStoredCmd, []string{covCredIDCli3})
		if err == nil || !strings.Contains(err.Error(), "validation failed") {
			t.Fatalf("expected generic validation error, got %v", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(path, clitest.ErrorResponse(500, "tester broke"))
		if err := credTestStoredCmd.RunE(credTestStoredCmd, []string{covCredIDCli3}); err == nil {
			t.Fatal("expected error")
		}
	})
}

// ─── default-env-var ────────────────────────────────────────────────────

func TestCredDefaultEnvVarCmd(t *testing.T) {
	t.Run("requires provider", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, credDefaultEnvVarCmd)
		err := credDefaultEnvVarCmd.RunE(credDefaultEnvVarCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "--provider is required") {
			t.Fatalf("expected provider-required error, got %v", err)
		}
	})

	t.Run("prints env var", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credDefaultEnvVarCmd)
		stub.OnGet("/api/v1/credentials/default-env-var",
			clitest.JSONResponse(200, map[string]string{"env_var": "GH_TOKEN"}))
		if err := credDefaultEnvVarCmd.Flags().Set("provider", "GITHUB"); err != nil {
			t.Fatal(err)
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := credDefaultEnvVarCmd.RunE(credDefaultEnvVarCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if strings.TrimSpace(out) != "GH_TOKEN" {
			t.Errorf("output: got %q want GH_TOKEN", out)
		}
		calls := stub.CallsFor("GET", "/api/v1/credentials/default-env-var")
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "provider=GITHUB") {
			t.Errorf("provider query missing: %+v", calls)
		}
		// Endpoint is workspace-agnostic — client clears the workspace.
		if strings.Contains(calls[0].Query, "workspace_id") {
			t.Errorf("workspace_id should NOT be sent: %q", calls[0].Query)
		}
	})

	t.Run("no default for provider", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credDefaultEnvVarCmd)
		stub.OnGet("/api/v1/credentials/default-env-var",
			clitest.JSONResponse(200, map[string]string{"env_var": ""}))
		if err := credDefaultEnvVarCmd.Flags().Set("provider", "WEIRD"); err != nil {
			t.Fatal(err)
		}
		err := credDefaultEnvVarCmd.RunE(credDefaultEnvVarCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "no default env var") {
			t.Fatalf("expected no-default error, got %v", err)
		}
	})
}
