package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covSetFlags sets each k=v on the command's flag set, failing the test
// on error. Pair with covResetFlags so the Changed bits don't leak.
func covSetFlags(t *testing.T, cmd *cobra.Command, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s=%s: %v", k, v, err)
		}
	}
}

// ─── credential create ──────────────────────────────────────────────────

func TestCredCreateCmd_Validation(t *testing.T) {
	cases := []struct {
		name    string
		flags   map[string]string
		wantErr string
	}{
		{"missing name", map[string]string{}, "--name is required"},
		{"missing type", map[string]string{"name": "x"}, "--type is required"},
		{"missing value", map[string]string{"name": "x", "type": "API_KEY"}, "--value or --value-stdin is required"},
		{"bad security level", map[string]string{"name": "x", "type": "API_KEY", "value": "v", "security-level": "7"},
			"--security-level must be between 0 and 3"},
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

func TestCredCreateCmd_HappyPath(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "gh-token"}))

	// Provider NONE → validation skipped entirely.
	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "gh-token", "type": "API_KEY", "provider": "NONE",
		"value": "secret-v", "env-var-name": "GH_TOKEN", "security-level": "2",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/credentials")
	if len(calls) != 1 {
		t.Fatalf("expected 1 create POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["name"] != "gh-token" || body["type"] != "API_KEY" || body["value"] != "secret-v" {
		t.Errorf("create body wrong: %v", body)
	}
	if body["provider"] != "NONE" || body["env_var_name"] != "GH_TOKEN" {
		t.Errorf("optional fields wrong: %v", body)
	}
	if lvl, ok := body["security_level"].(float64); !ok || lvl != 2 {
		t.Errorf("security_level wrong: %v", body["security_level"])
	}
}

func TestCredCreateCmd_ValueStdin(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "k"}))
	covWithStdin(t, "stdin-secret\n")

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "k", "type": "SECRET", "value-stdin": "true",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/credentials")[0].Body, &body)
	if body["value"] != "stdin-secret" {
		t.Errorf("stdin value not used: %v", body["value"])
	}
}

func TestCredCreateCmd_InvalidKeyNonInteractiveStillSaves(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnPost("/api/v1/credentials/test",
		clitest.JSONResponse(200, map[string]any{"valid": false, "error": "bad key"}))
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "k"}))
	// Test binary's stdin is not a TTY → warning + proceed without prompt.
	covWithStdin(t, "")

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "k", "type": "API_KEY", "provider": "GITHUB", "value": "ghp_bad",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
		t.Fatalf("RunE should proceed non-interactively: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/credentials")) != 1 {
		t.Error("credential should still be created after non-interactive warning")
	}
}

func TestCredCreateCmd_ServerError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnPost("/api/v1/credentials", clitest.ErrorResponse(409, "name already exists"))

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "dup", "type": "SECRET", "value": "v",
	})
	err := credCreateCmd.RunE(credCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

// ─── credential update ──────────────────────────────────────────────────

func TestCredUpdateCmd_NoFields(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUpdateCmd)
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Fatalf("expected no-fields error, got %v", err)
	}
}

func TestCredUpdateCmd_EmptyValueRejected(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUpdateCmd)
	covSetFlags(t, credUpdateCmd, map[string]string{"value": ""})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "--value cannot be empty") {
		t.Fatalf("expected empty-value error, got %v", err)
	}
}

func TestCredUpdateCmd_BadSecurityLevel(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUpdateCmd)
	covSetFlags(t, credUpdateCmd, map[string]string{"security-level": "9"})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "--security-level must be between 0 and 3") {
		t.Fatalf("expected security-level error, got %v", err)
	}
}

func TestCredUpdateCmd_NameOnly(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))

	covSetFlags(t, credUpdateCmd, map[string]string{"name": "renamed"})
	if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["name"] != "renamed" || len(body) != 1 {
		t.Errorf("PATCH body wrong: %v", body)
	}
}

func TestCredUpdateCmd_ValueWithMetadataValidation(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3,
		clitest.JSONResponse(200, map[string]string{"type": "API_KEY", "provider": "GITHUB"}))
	stub.OnPost("/api/v1/credentials/test",
		clitest.JSONResponse(200, map[string]any{"valid": true}))
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))

	covSetFlags(t, credUpdateCmd, map[string]string{"value": "ghp_new"})
	if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/credentials/test")) != 1 {
		t.Error("expected pre-save validation POST")
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)[0].Body, &body)
	if body["value"] != "ghp_new" {
		t.Errorf("PATCH body wrong: %v", body)
	}
}

func TestCredUpdateCmd_MetadataFetchFailsStillUpdates(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.ErrorResponse(500, "meta broke"))
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))

	covSetFlags(t, credUpdateCmd, map[string]string{"value": "v2"})
	if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE should warn-and-continue on metadata failure: %v", err)
	}
	if len(stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)) != 1 {
		t.Error("PATCH should still fire when metadata fetch fails")
	}
}

func TestCredUpdateCmd_StdinEmptyMeansNoFields(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUpdateCmd)
	covWithStdin(t, "") // EOF — scanner.Scan() returns false
	covSetFlags(t, credUpdateCmd, map[string]string{"value-stdin": "true"})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Fatalf("expected no-fields error, got %v", err)
	}
}

func TestCredUpdateCmd_StdinBlankLineRejected(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUpdateCmd)
	covWithStdin(t, "\n") // a scanned-but-empty line
	covSetFlags(t, credUpdateCmd, map[string]string{"value-stdin": "true"})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "stdin value cannot be empty") {
		t.Fatalf("expected stdin-empty error, got %v", err)
	}
}

// ─── rotate ─────────────────────────────────────────────────────────────

func TestCredRotateCmd_RequiresValue(t *testing.T) {
	covStub(t)
	covResetFlags(t, credRotateCmd)
	err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "--value or --value-stdin is required") {
		t.Fatalf("expected value-required error, got %v", err)
	}
}

func TestCredRotateCmd_AbortedWithoutConfirmation(t *testing.T) {
	covStub(t)
	covResetFlags(t, credRotateCmd)
	covWithStdin(t, "n\n") // non-TTY confirm fallback reads stdin
	covSetFlags(t, credRotateCmd, map[string]string{"value": "newv"})
	err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted, got %v", err)
	}
}

func TestCredRotateCmd_BadGraceSeconds(t *testing.T) {
	covStub(t)
	covResetFlags(t, credRotateCmd)
	covSetFlags(t, credRotateCmd, map[string]string{
		"value": "newv", "yes": "true", "grace-seconds": "999999999",
	})
	err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "--grace-seconds must be between") {
		t.Fatalf("expected grace-seconds error, got %v", err)
	}
}

func TestCredRotateCmd_HappyPath(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credRotateCmd)
	stub.OnPost("/api/v1/credentials/"+covCredIDCli3+"/rotate",
		clitest.JSONResponse(200, map[string]any{
			"id": "rot1", "status": "ACTIVE", "grace_seconds": 120, "expires_at": "2026-06-02T00:00:00Z",
		}))

	covSetFlags(t, credRotateCmd, map[string]string{
		"value": "rotated-v", "yes": "true", "grace-seconds": "120",
	})
	if err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/credentials/"+covCredIDCli3+"/rotate")
	if len(calls) != 1 {
		t.Fatalf("expected 1 rotate POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["value"] != "rotated-v" {
		t.Errorf("rotate body value: %v", body)
	}
	if gs, ok := body["grace_seconds"].(float64); !ok || gs != 120 {
		t.Errorf("grace_seconds: %v", body["grace_seconds"])
	}
}

func TestCredRotateCmd_ValueStdin(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credRotateCmd)
	stub.OnPost("/api/v1/credentials/"+covCredIDCli3+"/rotate",
		clitest.JSONResponse(200, map[string]any{"id": "rot2", "status": "ACTIVE"}))
	covWithStdin(t, "stdin-rotated\n")
	covSetFlags(t, credRotateCmd, map[string]string{"value-stdin": "true", "yes": "true"})

	if err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/credentials/"+covCredIDCli3+"/rotate")[0].Body, &body)
	if body["value"] != "stdin-rotated" {
		t.Errorf("stdin rotate value: %v", body)
	}
}

func TestCredRotateCmd_ServerError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credRotateCmd)
	stub.OnPost("/api/v1/credentials/"+covCredIDCli3+"/rotate",
		clitest.ErrorResponse(409, "rotation already active"))
	covSetFlags(t, credRotateCmd, map[string]string{"value": "v", "yes": "true"})
	err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "rotation already active") {
		t.Fatalf("expected 409 error, got %v", err)
	}
}

// ─── rotation-cancel ────────────────────────────────────────────────────

func TestCredRotationCancelCmd(t *testing.T) {
	path := "/api/v1/credential-rotations/rot1"

	run := func(t *testing.T, resp map[string]any) string {
		t.Helper()
		stub := covStub(t)
		covResetFlags(t, credRotationCancelCmd)
		stub.OnDelete(path, clitest.JSONResponse(200, resp))
		covSetFlags(t, credRotationCancelCmd, map[string]string{"yes": "true"})
		return covCaptureStderr(t, func() {
			if err := credRotationCancelCmd.RunE(credRotationCancelCmd, []string{"rot1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
	}

	t.Run("default cancelled message", func(t *testing.T) {
		out := run(t, map[string]any{"status": "CANCELLED"})
		if !strings.Contains(out, "cancelled") {
			t.Errorf("expected cancelled message: %q", out)
		}
	})
	t.Run("message branch", func(t *testing.T) {
		out := run(t, map[string]any{"status": "EXPIRED", "message": "grace already over"})
		if !strings.Contains(out, "grace already over") {
			t.Errorf("expected message surfaced: %q", out)
		}
	})
	t.Run("non-cancelled status branch", func(t *testing.T) {
		out := run(t, map[string]any{"status": "EXPIRED"})
		if !strings.Contains(out, "EXPIRED") {
			t.Errorf("expected status surfaced: %q", out)
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credRotationCancelCmd)
		stub.OnDelete(path, clitest.ErrorResponse(404, "rotation not found"))
		covSetFlags(t, credRotationCancelCmd, map[string]string{"yes": "true"})
		err := credRotationCancelCmd.RunE(credRotationCancelCmd, []string{"rot1"})
		if err == nil || !strings.Contains(err.Error(), "rotation not found") {
			t.Fatalf("expected 404 error, got %v", err)
		}
	})
}

// ─── delete ─────────────────────────────────────────────────────────────

func TestCredDeleteCmd(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credDeleteCmd)
		stub.OnDelete("/api/v1/credentials/"+covCredIDCli3, clitest.EmptyResponse(204))
		covSetFlags(t, credDeleteCmd, map[string]string{"yes": "true"})
		if err := credDeleteCmd.RunE(credDeleteCmd, []string{covCredIDCli3}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if len(stub.CallsFor("DELETE", "/api/v1/credentials/"+covCredIDCli3)) != 1 {
			t.Error("expected 1 DELETE")
		}
	})
	t.Run("aborted", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, credDeleteCmd)
		covWithStdin(t, "n\n")
		err := credDeleteCmd.RunE(credDeleteCmd, []string{covCredIDCli3})
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("expected aborted, got %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credDeleteCmd)
		stub.OnDelete("/api/v1/credentials/"+covCredIDCli3, clitest.ErrorResponse(409, "credential in use"))
		covSetFlags(t, credDeleteCmd, map[string]string{"yes": "true"})
		err := credDeleteCmd.RunE(credDeleteCmd, []string{covCredIDCli3})
		if err == nil || !strings.Contains(err.Error(), "credential in use") {
			t.Fatalf("expected conflict error, got %v", err)
		}
	})
}
