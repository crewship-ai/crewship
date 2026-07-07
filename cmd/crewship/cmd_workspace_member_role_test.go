package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity for `crewship workspace member role` (#867.2). Drives the
// command against a stub and asserts it issues PATCH
// /api/v1/workspaces/{id}/members/{memberId} with the uppercased role.

func TestWorkspaceMemberRole_RunE_SendsPatch(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPatch("/api/v1/workspaces/"+covWS+"/members/mem123",
		clitest.JSONResponse(200, map[string]string{"status": "updated", "role": "MANAGER"}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		// lower-case role arg must be normalised to upper-case.
		if err := workspaceMemberRoleCmd.RunE(workspaceMemberRoleCmd, []string{"mem123", "manager"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("PATCH", "/api/v1/workspaces/"+covWS+"/members/mem123")
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 PATCH, got %d", len(calls))
	}
	var body struct {
		Role string `json:"role"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.Role != "MANAGER" {
		t.Fatalf("role = %q, want MANAGER", body.Role)
	}
	if !strings.Contains(out, "MANAGER") {
		t.Errorf("expected success message, got: %s", out)
	}
}

// A server-side ladder rejection (403) surfaces as a command error.
func TestWorkspaceMemberRole_RunE_SurfacesForbidden(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPatch("/api/v1/workspaces/"+covWS+"/members/mem123",
		clitest.ErrorResponse(403, "Forbidden"))
	setStubCLI(t, stub.URL())

	captureStdoutCovCli2(t, func() {
		if err := workspaceMemberRoleCmd.RunE(workspaceMemberRoleCmd, []string{"mem123", "ADMIN"}); err == nil {
			t.Errorf("expected error on 403 from server")
		}
	})
}
