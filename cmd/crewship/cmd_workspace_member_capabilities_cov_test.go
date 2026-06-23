package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covCapsPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/members/u-1/capabilities"

func TestWorkspaceMemberCapsListRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covCapsPath, clitest.JSONResponse(200, map[string]any{
		"user_id": "u-1", "role": "MEMBER", "capabilities": []string{"chat", "routine.create"},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return workspaceMemberCapsListCmd.RunE(workspaceMemberCapsListCmd, []string{"u-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "u-1") || !strings.Contains(out, "MEMBER") || !strings.Contains(out, "chat, routine.create") {
		t.Errorf("caps row missing:\n%s", out)
	}
}

func TestWorkspaceMemberCapsListRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := workspaceMemberCapsListCmd.RunE(workspaceMemberCapsListCmd, []string{"u-1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestWorkspaceMemberCapsGrantRunE_PatchBody(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covCapsPath, clitest.JSONResponse(200, map[string]any{
		"user_id": "u-1", "role": "MEMBER", "capabilities": []string{"chat", "routine.create", "issue.create"},
	}))
	covSetupCli10(t, s.URL())

	stderr, err := captureStderrCov(t, func() error {
		return workspaceMemberCapsGrantCmd.RunE(workspaceMemberCapsGrantCmd,
			[]string{"u-1", "routine.create", "issue.create"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PATCH", covCapsPath)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d", len(calls))
	}
	if got := string(calls[0].Body); !strings.Contains(got, `"grant":["routine.create","issue.create"]`) {
		t.Errorf("grant body wrong: %s", got)
	}
	if !strings.Contains(stderr, "u-1 (MEMBER): chat, routine.create, issue.create") {
		t.Errorf("success line wrong: %q", stderr)
	}
}

func TestWorkspaceMemberCapsRevokeRunE_PatchBody(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covCapsPath, clitest.JSONResponse(200, map[string]any{
		"user_id": "u-1", "role": "MEMBER", "capabilities": []string{"chat"},
	}))
	covSetupCli10(t, s.URL())

	if _, err := captureStderrCov(t, func() error {
		return workspaceMemberCapsRevokeCmd.RunE(workspaceMemberCapsRevokeCmd,
			[]string{"u-1", "routine.create"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PATCH", covCapsPath)
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"revoke":["routine.create"]`) {
		t.Errorf("revoke body wrong: %+v", calls)
	}
}

func TestWorkspaceMemberCapsPresetRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covCapsPath, clitest.JSONResponse(200, map[string]any{
		"user_id": "u-1", "role": "MEMBER", "capabilities": []string{"chat", "routine.create", "issue.create", "memory.write"},
	}))
	covSetupCli10(t, s.URL())

	if _, err := captureStderrCov(t, func() error {
		return workspaceMemberCapsPresetCmd.RunE(workspaceMemberCapsPresetCmd, []string{"u-1", "POWER"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PATCH", covCapsPath)
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"preset":"power"`) {
		t.Errorf("preset body wrong (must lowercase): %+v", calls)
	}
}

func TestWorkspaceMemberCapsPresetRunE_UnknownPreset(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	err := workspaceMemberCapsPresetCmd.RunE(workspaceMemberCapsPresetCmd, []string{"u-1", "superuser"})
	if err == nil || !strings.Contains(err.Error(), `unknown preset "superuser"`) {
		t.Errorf("expected preset validation error, got %v", err)
	}
}

func TestMutateCaps_RejectsUnknownOp(t *testing.T) {
	err := mutateCaps("u-1", "escalate", []string{"chat"})
	if err == nil || !strings.Contains(err.Error(), `unknown op "escalate"`) {
		t.Errorf("expected internal op guard, got %v", err)
	}
}

func TestPatchCaps_MarshalErrorSurfaced(t *testing.T) {
	// Channels are not JSON-marshallable — exercises patchCaps' own
	// error path before any network call happens.
	err := patchCaps("u-1", map[string]any{"bad": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("expected marshal error, got %v", err)
	}
}

func TestPatchCapsRaw_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := patchCapsRaw("u-1", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestWorkspaceMemberCapsListRunE_GuardsAndDecodeErrors(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := workspaceMemberCapsListCmd.RunE(workspaceMemberCapsListCmd, []string{"u-1"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}

	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	if err := workspaceMemberCapsListCmd.RunE(workspaceMemberCapsListCmd, []string{"u-1"}); err == nil {
		t.Error("expected transport error")
	}

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covCapsPath, clitest.TextResponse(200, `}{`))
	covSetupCli10(t, s.URL())
	if err := workspaceMemberCapsListCmd.RunE(workspaceMemberCapsListCmd, []string{"u-1"}); err == nil {
		t.Error("expected decode error")
	}
}

func TestWorkspaceMemberCapsListRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covCapsPath, clitest.ErrorResponse(403, "members only"))
	covSetupCli10(t, s.URL())
	err := workspaceMemberCapsListCmd.RunE(workspaceMemberCapsListCmd, []string{"u-1"})
	if err == nil || !strings.Contains(err.Error(), "members only") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}

func TestPatchCapsRaw_NoAuthTransportAndDecodeErrors(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := patchCapsRaw("u-1", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}

	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	if err := patchCapsRaw("u-1", []byte(`{}`)); err == nil {
		t.Error("expected transport error")
	}

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covCapsPath, clitest.TextResponse(200, `nope`))
	covSetupCli10(t, s.URL())
	if err := patchCapsRaw("u-1", []byte(`{}`)); err == nil {
		t.Error("expected decode error")
	}
}

func TestPatchCapsRaw_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covCapsPath, clitest.ErrorResponse(403, "ADMIN required"))
	covSetupCli10(t, s.URL())
	err := patchCapsRaw("u-1", []byte(`{"grant":["chat"]}`))
	if err == nil || !strings.Contains(err.Error(), "ADMIN required") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}
