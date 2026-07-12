//go:build !clionly

package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestAdminReencrypt_NoAuth(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{{name: "reencrypt", cmd: adminReencryptCmd}})
}

func TestAdminReencrypt_NoWorkspace(t *testing.T) {
	covRunNoWorkspace(t, []covCmdCase{{name: "reencrypt", cmd: adminReencryptCmd}})
}

func TestAdminReencrypt_HappyPath(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/admin/reencrypt", clitest.JSONResponse(200, map[string]any{
		"key_version": "v2",
		"reencrypted": 12,
		"skipped":     3,
		"failed":      0,
		"columns": []map[string]any{
			{"table": "credentials", "column": "encrypted_value", "reencrypted": 12, "skipped": 3, "failed": 0},
		},
	}))
	if err := adminReencryptCmd.RunE(adminReencryptCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/admin/reencrypt"); len(calls) != 1 {
		t.Fatalf("expected 1 POST to reencrypt endpoint, got %d", len(calls))
	}
}

// TestAdminReencrypt_FailedRowsExit: failed > 0 means some envelopes are
// still on an unknown/old key — the command must exit non-zero so a scripted
// rotation doesn't retire the old key on a false success.
func TestAdminReencrypt_FailedRowsExit(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/admin/reencrypt", clitest.JSONResponse(200, map[string]any{
		"key_version": "v2",
		"reencrypted": 5,
		"skipped":     0,
		"failed":      2,
		"columns":     []map[string]any{},
	}))
	err := adminReencryptCmd.RunE(adminReencryptCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("expected failure mentioning 2 failed values, got %v", err)
	}
}

func TestAdminReencrypt_ServerError(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/admin/reencrypt", clitest.ErrorResponse(500, "re-encryption aborted: ENCRYPTION_KEY_V2 is not set"))
	err := adminReencryptCmd.RunE(adminReencryptCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY_V2") {
		t.Fatalf("expected server error surfaced, got %v", err)
	}
}
