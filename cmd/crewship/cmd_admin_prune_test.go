package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestAdminPruneLegacy_NoAuth(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{{name: "prune-legacy", cmd: adminPruneLegacyCmd}})
}

func TestAdminPruneLegacy_NoWorkspace(t *testing.T) {
	covRunNoWorkspace(t, []covCmdCase{{name: "prune-legacy", cmd: adminPruneLegacyCmd}})
}

func TestAdminPruneLegacy_HappyPath(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/admin/prune-legacy-resources", clitest.JSONResponse(200, map[string]any{
		"removed": []string{"crewship-3-tools-engineering", "crewship-3-home-engineering"},
		"count":   2,
	}))
	if err := adminPruneLegacyCmd.RunE(adminPruneLegacyCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/admin/prune-legacy-resources")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST to prune endpoint, got %d", len(calls))
	}
}

func TestAdminPruneLegacy_ServerError(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/admin/prune-legacy-resources", clitest.ErrorResponse(503, "docker not configured"))
	err := adminPruneLegacyCmd.RunE(adminPruneLegacyCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "docker not configured") {
		t.Fatalf("expected 503 surfaced, got %v", err)
	}
}
