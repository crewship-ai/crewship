package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCredentialMutateCmds_NoAuth(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{
		{name: "create", cmd: credCreateCmd},
		{name: "update", cmd: credUpdateCmd, args: []string{covCredIDCli3}},
		{name: "rotate", cmd: credRotateCmd, args: []string{covCredIDCli3}},
		{name: "rotation-cancel", cmd: credRotationCancelCmd, args: []string{"rot1"}},
		{name: "delete", cmd: credDeleteCmd, args: []string{covCredIDCli3}},
	})
}

func TestCredentialMutateCmds_NoWorkspace(t *testing.T) {
	covRunNoWorkspace(t, []covCmdCase{
		{name: "create", cmd: credCreateCmd},
		{name: "update", cmd: credUpdateCmd, args: []string{covCredIDCli3}},
		{name: "rotate", cmd: credRotateCmd, args: []string{covCredIDCli3}},
		{name: "rotation-cancel", cmd: credRotationCancelCmd, args: []string{"rot1"}},
		{name: "delete", cmd: credDeleteCmd, args: []string{covCredIDCli3}},
	})
}

func TestCredentialMutateCmds_TransportError(t *testing.T) {
	covRunTransportError(t, []covCmdCase{
		// create: validation POST fails (transport) → warn-and-continue
		// (stdin is a pipe, non-TTY) → final create POST also fails.
		{name: "create", cmd: credCreateCmd,
			flags: map[string]string{"name": "n", "type": "API_KEY", "provider": "GITHUB", "value": "v"}},
		// update: metadata GET fails (warning) → PATCH fails.
		{name: "update", cmd: credUpdateCmd, args: []string{covCredIDCli3},
			flags: map[string]string{"value": "v2"}},
		{name: "rotate", cmd: credRotateCmd, args: []string{covCredIDCli3},
			flags: map[string]string{"value": "v", "yes": "true"}},
		{name: "rotate resolve by name", cmd: credRotateCmd, args: []string{"by-name"},
			flags: map[string]string{"value": "v", "yes": "true"}},
		{name: "rotation-cancel", cmd: credRotationCancelCmd, args: []string{"rot1"},
			flags: map[string]string{"yes": "true"}},
		{name: "delete", cmd: credDeleteCmd, args: []string{covCredIDCli3},
			flags: map[string]string{"yes": "true"}},
		{name: "delete resolve by name", cmd: credDeleteCmd, args: []string{"by-name"},
			flags: map[string]string{"yes": "true"}},
	})
}

// ─── create extras ──────────────────────────────────────────────────────

func TestCredCreateCmd_InvalidKeyEmptyServerMessage(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	// valid:false without an error string → CLI substitutes the default
	// "key validation failed" message; non-TTY → warn and proceed.
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": false}))
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "k"}))
	covWithStdin(t, "")

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "k", "type": "API_KEY", "provider": "GITHUB", "value": "bad",
	})
	stderr := covCaptureStderr(t, func() {
		if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "key validation failed") {
		t.Errorf("default validation message missing: %q", stderr)
	}
}

func TestCredCreateCmd_MalformedCreateResponse(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnPost("/api/v1/credentials", clitest.TextResponse(201, "{broken"))
	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "k", "type": "SECRET", "value": "v",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err == nil {
		t.Fatal("expected decode error")
	}
}

// ─── update extras ──────────────────────────────────────────────────────

func TestCredUpdateCmd_ResolveFails(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials", clitest.ErrorResponse(500, "resolver broke"))
	covSetFlags(t, credUpdateCmd, map[string]string{"name": "x"})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{"by-name"})
	if err == nil || !strings.Contains(err.Error(), "resolver broke") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

func TestCredUpdateCmd_ValueViaStdin(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3,
		clitest.JSONResponse(200, map[string]string{"type": "API_KEY", "provider": "GITHUB"}))
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": true}))
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))
	covWithStdin(t, "stdin-new-value\n")

	covSetFlags(t, credUpdateCmd, map[string]string{"value-stdin": "true"})
	if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)[0].Body, &body)
	if body["value"] != "stdin-new-value" {
		t.Errorf("stdin value not in PATCH body: %v", body)
	}
}

func TestCredUpdateCmd_SecurityLevel(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))

	covSetFlags(t, credUpdateCmd, map[string]string{"security-level": "3"})
	if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)[0].Body, &body)
	if lvl, ok := body["security_level"].(float64); !ok || lvl != 3 {
		t.Errorf("security_level wrong: %v", body)
	}
}

func TestCredUpdateCmd_MalformedMetadataSkipsValidation(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.TextResponse(200, "{broken"))
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))

	covSetFlags(t, credUpdateCmd, map[string]string{"value": "v2"})
	if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
		t.Fatalf("RunE should warn-and-continue on metadata parse failure: %v", err)
	}
	// Validation endpoint must NOT have been called.
	if len(stub.CallsFor("POST", "/api/v1/credentials/test")) != 0 {
		t.Error("validation should be skipped when metadata is unreadable")
	}
	if len(stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)) != 1 {
		t.Error("PATCH should still fire")
	}
}

func TestCredUpdateCmd_InvalidKeyEmptyMessageNonTTY(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3,
		clitest.JSONResponse(200, map[string]string{"type": "API_KEY", "provider": "GITHUB"}))
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": false}))
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))
	covWithStdin(t, "")

	covSetFlags(t, credUpdateCmd, map[string]string{"value": "bad-key"})
	stderr := covCaptureStderr(t, func() {
		if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "key validation failed") {
		t.Errorf("default message missing: %q", stderr)
	}
	if len(stub.CallsFor("PATCH", "/api/v1/credentials/"+covCredIDCli3)) != 1 {
		t.Error("update should proceed non-interactively")
	}
}

func TestCredUpdateCmd_PatchRejected(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.ErrorResponse(409, "name taken"))
	covSetFlags(t, credUpdateCmd, map[string]string{"name": "dup"})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "name taken") {
		t.Fatalf("expected 409, got %v", err)
	}
}

// ─── rotate / rotation-cancel extras ────────────────────────────────────

func TestCredRotateCmd_MalformedResponse(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credRotateCmd)
	stub.OnPost("/api/v1/credentials/"+covCredIDCli3+"/rotate", clitest.TextResponse(200, "{broken"))
	covSetFlags(t, credRotateCmd, map[string]string{"value": "v", "yes": "true"})
	if err := credRotateCmd.RunE(credRotateCmd, []string{covCredIDCli3}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestCredRotationCancelCmd_Aborted(t *testing.T) {
	covStub(t)
	covResetFlags(t, credRotationCancelCmd)
	covWithStdin(t, "n\n")
	err := credRotationCancelCmd.RunE(credRotationCancelCmd, []string{"rot1"})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted, got %v", err)
	}
}

func TestCredRotationCancelCmd_MalformedResponse(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credRotationCancelCmd)
	stub.OnDelete("/api/v1/credential-rotations/rot1", clitest.TextResponse(200, "{broken"))
	covSetFlags(t, credRotationCancelCmd, map[string]string{"yes": "true"})
	if err := credRotationCancelCmd.RunE(credRotationCancelCmd, []string{"rot1"}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestCredDeleteCmd_ResolveFails(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credDeleteCmd)
	stub.OnGet("/api/v1/credentials", clitest.ErrorResponse(500, "resolver broke"))
	covSetFlags(t, credDeleteCmd, map[string]string{"yes": "true"})
	err := credDeleteCmd.RunE(credDeleteCmd, []string{"by-name"})
	if err == nil || !strings.Contains(err.Error(), "resolver broke") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}
