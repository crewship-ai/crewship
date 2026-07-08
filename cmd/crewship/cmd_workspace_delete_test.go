package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity test for `crewship workspace delete` (#866.2, CLAUDE.md
// core rule #3 — every endpoint gets a CLI command with an acceptance
// test that drives the endpoint through the command, not a hand-rolled
// request).

// Happy path: the command issues DELETE /api/v1/workspaces/{id} with the
// re-typed slug in the confirm_slug body.
func TestWorkspaceDelete_RunE_SendsConfirmSlug(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnDelete("/api/v1/workspaces/"+covWS, clitest.JSONResponse(200, map[string]bool{"success": true}))
	setStubCLI(t, stub.URL())

	if err := workspaceDeleteCmd.Flags().Set("confirm", "acme"); err != nil {
		t.Fatalf("set confirm: %v", err)
	}
	if err := workspaceDeleteCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set yes: %v", err)
	}
	t.Cleanup(func() {
		_ = workspaceDeleteCmd.Flags().Set("confirm", "")
		_ = workspaceDeleteCmd.Flags().Set("yes", "false")
	})

	out := captureStdoutCovCli2(t, func() {
		if err := workspaceDeleteCmd.RunE(workspaceDeleteCmd, []string{covWS}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWS)
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 DELETE, got %d", len(calls))
	}
	var body struct {
		ConfirmSlug string `json:"confirm_slug"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.ConfirmSlug != "acme" {
		t.Fatalf("confirm_slug = %q, want %q", body.ConfirmSlug, "acme")
	}
	if !strings.Contains(out, "deleted") {
		t.Errorf("expected success message, got: %s", out)
	}
}

// Without --confirm the command must refuse locally and never touch the
// server — the type-the-slug guard is client-side too.
func TestWorkspaceDelete_RunE_RequiresConfirm(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())

	_ = workspaceDeleteCmd.Flags().Set("confirm", "")
	_ = workspaceDeleteCmd.Flags().Set("yes", "true")
	t.Cleanup(func() { _ = workspaceDeleteCmd.Flags().Set("yes", "false") })

	err := workspaceDeleteCmd.RunE(workspaceDeleteCmd, []string{covWS})
	if err == nil {
		t.Fatalf("expected an error when --confirm is missing")
	}
	if !strings.Contains(err.Error(), "confirm") {
		t.Errorf("error = %q, want mention of confirm", err.Error())
	}
	if len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWS)) != 0 {
		t.Errorf("no server call should be made without --confirm")
	}
}

// A server-side rejection (e.g. 409 last-workspace, 403 non-owner) must
// surface as a non-nil error from the command.
func TestWorkspaceDelete_RunE_SurfacesServerError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnDelete("/api/v1/workspaces/"+covWS, clitest.ErrorResponse(409, "Cannot delete your only workspace"))
	setStubCLI(t, stub.URL())

	_ = workspaceDeleteCmd.Flags().Set("confirm", "acme")
	_ = workspaceDeleteCmd.Flags().Set("yes", "true")
	t.Cleanup(func() {
		_ = workspaceDeleteCmd.Flags().Set("confirm", "")
		_ = workspaceDeleteCmd.Flags().Set("yes", "false")
	})

	captureStdoutCovCli2(t, func() {
		if err := workspaceDeleteCmd.RunE(workspaceDeleteCmd, []string{covWS}); err == nil {
			t.Errorf("expected error on 409 from server")
		}
	})
}
