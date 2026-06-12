package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func adminExtrasGuardCases() []covCmdCase {
	return []covCmdCase{
		{name: "triage list", cmd: triageListCmd},
		{name: "triage process", cmd: triageProcessCmd},
		{name: "triage create", cmd: triageCreateCmd},
		{name: "triage update", cmd: triageUpdateCmd, args: []string{"tr1"}},
		{name: "triage delete", cmd: triageDeleteCmd, args: []string{"tr1"},
			flags: map[string]string{"yes": "true"}},
		{name: "recurring list", cmd: recurringListCmd},
		{name: "recurring create", cmd: recurringCreateCmd},
		{name: "recurring update", cmd: recurringUpdateCmd, args: []string{"r1"}},
		{name: "recurring delete", cmd: recurringDeleteCmd, args: []string{"r1"}},
		{name: "saved-view list", cmd: savedViewListCmd},
		{name: "saved-view create", cmd: savedViewCreateCmd},
		{name: "saved-view update", cmd: savedViewUpdateCmd, args: []string{"v1"}},
		{name: "saved-view delete", cmd: savedViewDeleteCmd, args: []string{"v1"}},
		{name: "mcp-calls", cmd: mcpCallsCmd},
		{name: "metrics", cmd: metricsCmd},
	}
}

// Every admin-extras command must short-circuit on missing auth before
// any flag validation or network traffic happens.
func TestAdminExtrasCmds_NoAuth(t *testing.T) {
	covRunNoAuth(t, adminExtrasGuardCases())
}

func TestAdminExtrasCmds_NoWorkspace(t *testing.T) {
	covRunNoWorkspace(t, adminExtrasGuardCases())
}

// Server-side failures surface verbatim for the wrapper commands that
// have no client-side validation between the guard and the request.
func TestAdminExtrasCmds_ServerErrors(t *testing.T) {
	t.Run("triage list", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/triage-rules", clitest.ErrorResponse(500, "rules broke"))
		err := triageListCmd.RunE(triageListCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "rules broke") {
			t.Fatalf("expected 500, got %v", err)
		}
	})
	t.Run("triage process", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost("/api/v1/triage/process", clitest.ErrorResponse(409, "already running"))
		err := triageProcessCmd.RunE(triageProcessCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("expected 409, got %v", err)
		}
	})
	t.Run("triage create", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, triageCreateCmd)
		stub.OnPost("/api/v1/triage-rules", clitest.ErrorResponse(422, "pattern invalid"))
		covSetFlags(t, triageCreateCmd, map[string]string{
			"name": "n", "pattern": "(", "match-type": "regex",
		})
		err := triageCreateCmd.RunE(triageCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "pattern invalid") {
			t.Fatalf("expected 422, got %v", err)
		}
	})
	t.Run("triage update", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, triageUpdateCmd)
		stub.OnPatch("/api/v1/triage-rules/tr1", clitest.ErrorResponse(404, "rule gone"))
		covSetFlags(t, triageUpdateCmd, map[string]string{"name": "n"})
		err := triageUpdateCmd.RunE(triageUpdateCmd, []string{"tr1"})
		if err == nil || !strings.Contains(err.Error(), "rule gone") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
	t.Run("recurring create", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, recurringCreateCmd)
		stub.OnPost("/api/v1/recurring-issues", clitest.ErrorResponse(422, "bad cron"))
		covSetFlags(t, recurringCreateCmd, map[string]string{
			"crew": "c", "title": "t", "cron": "bad",
		})
		err := recurringCreateCmd.RunE(recurringCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "bad cron") {
			t.Fatalf("expected 422, got %v", err)
		}
	})
	t.Run("recurring update", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, recurringUpdateCmd)
		stub.OnPatch("/api/v1/recurring-issues/r1", clitest.ErrorResponse(404, "schedule gone"))
		covSetFlags(t, recurringUpdateCmd, map[string]string{"title": "t2"})
		err := recurringUpdateCmd.RunE(recurringUpdateCmd, []string{"r1"})
		if err == nil || !strings.Contains(err.Error(), "schedule gone") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
	t.Run("saved-view create", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, savedViewCreateCmd)
		stub.OnPost("/api/v1/saved-views", clitest.ErrorResponse(409, "view exists"))
		covSetFlags(t, savedViewCreateCmd, map[string]string{"name": "n", "filters": "{}"})
		err := savedViewCreateCmd.RunE(savedViewCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "view exists") {
			t.Fatalf("expected 409, got %v", err)
		}
	})
	t.Run("saved-view update", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, savedViewUpdateCmd)
		stub.OnPatch("/api/v1/saved-views/v1", clitest.ErrorResponse(403, "not the owner"))
		covSetFlags(t, savedViewUpdateCmd, map[string]string{"name": "n2"})
		err := savedViewUpdateCmd.RunE(savedViewUpdateCmd, []string{"v1"})
		if err == nil || !strings.Contains(err.Error(), "not the owner") {
			t.Fatalf("expected 403, got %v", err)
		}
	})
	t.Run("saved-view delete", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete("/api/v1/saved-views/v1", clitest.ErrorResponse(404, "view gone"))
		err := savedViewDeleteCmd.RunE(savedViewDeleteCmd, []string{"v1"})
		if err == nil || !strings.Contains(err.Error(), "view gone") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
	t.Run("mcp-calls", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, mcpCallsCmd)
		stub.OnGet("/api/v1/mcp-tool-calls", clitest.ErrorResponse(500, "audit broke"))
		err := mcpCallsCmd.RunE(mcpCallsCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "audit broke") {
			t.Fatalf("expected 500, got %v", err)
		}
	})
	t.Run("metrics", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, metricsCmd)
		stub.OnGet("/api/v1/mission-metrics", clitest.ErrorResponse(500, "metrics broke"))
		err := metricsCmd.RunE(metricsCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "metrics broke") {
			t.Fatalf("expected 500, got %v", err)
		}
	})
	t.Run("recurring list", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/recurring-issues", clitest.ErrorResponse(500, "list broke"))
		if err := recurringListCmd.RunE(recurringListCmd, nil); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("saved-view list", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/saved-views", clitest.ErrorResponse(500, "list broke"))
		if err := savedViewListCmd.RunE(savedViewListCmd, nil); err == nil {
			t.Fatal("expected error")
		}
	})
}
