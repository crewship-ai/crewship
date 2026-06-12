package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// decideApproval error-path coverage. Happy paths are exercised by the
// existing cmd_approvals_test.go; these pin the guard rails.

func TestDecideApproval_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := decideApproval(approvalsApproveCmd, "apr-1", "approved")
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestDecideApproval_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := decideApproval(approvalsApproveCmd, "apr-1", "approved")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestDecideApproval_ServerErrorSurfaced(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/approvals/apr-9/decide", clitest.ErrorResponse(403, "requires OWNER or ADMIN"))
	covSetupCli10(t, s.URL())

	err := decideApproval(approvalsDenyCmd, "apr-9", "denied")
	if err == nil || !strings.Contains(err.Error(), "requires OWNER or ADMIN") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}

func TestDecideApproval_OmitsEmptyComment(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/approvals/apr-2/decide", clitest.JSONResponse(200, map[string]string{
		"status": "denied", "decided_by": "u-1",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, approvalsDenyCmd, "comment", "")

	out, err := captureStderrCov(t, func() error {
		return decideApproval(approvalsDenyCmd, "apr-2", "denied")
	})
	if err != nil {
		t.Fatalf("decideApproval: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/approvals/apr-2/decide")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if strings.Contains(string(calls[0].Body), "comment") {
		t.Errorf("empty comment must be omitted from body: %s", calls[0].Body)
	}
	if !strings.Contains(out, "Approval apr-2: denied (by u-1)") {
		t.Errorf("success line wrong: %q", out)
	}
}

func TestApprovalsGetRunE_TextModePrintsCanonicalThenExtraKeys(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals/apr-7", clitest.JSONResponse(200, map[string]any{
		"id": "apr-7", "status": "pending", "kind": "shell.exec",
		"reason": "rm -rf /tmp/x", "crew_id": "cc1",
		"custom_payload": "extra-data", "empty_field": nil,
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-7"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"apr-7", "pending", "shell.exec", "rm -rf /tmp/x", "custom_payload", "extra-data"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "empty_field") {
		t.Errorf("nil fields must be skipped:\n%s", out)
	}
}

func TestApprovalsGetRunE_JSONFormat(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals/apr-8", clitest.JSONResponse(200, map[string]any{
		"id": "apr-8", "status": "approved",
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-8"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"apr-8"`) || !strings.Contains(out, `"approved"`) {
		t.Errorf("json output missing:\n%s", out)
	}
}

func TestApprovalsGetRunE_NoAuthAndServerError(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-1"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals/apr-x", clitest.ErrorResponse(404, "approval not found"))
	covSetupCli10(t, s.URL())
	if err := approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-x"}); err == nil || !strings.Contains(err.Error(), "approval not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

func TestApprovalsResetAutoTuningRunE_PostsTool(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/approvals/reset-auto-tuning", clitest.JSONResponse(200, map[string]any{
		"tool": "shell.exec", "rows_deleted": 12,
	}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		var runErr error
		_, runErr = captureStderrCov(t, func() error {
			return approvalsResetAutoTuningCmd.RunE(approvalsResetAutoTuningCmd, []string{"shell.exec"})
		})
		return runErr
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/approvals/reset-auto-tuning")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"tool":"shell.exec"`) {
		t.Errorf("reset body wrong: %+v", calls)
	}
}

func TestApprovalsGetRunE_NoWorkspaceAndYAML(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-1"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals/apr-y", clitest.JSONResponse(200, map[string]any{"id": "apr-y", "status": "pending"}))
	covSetupCli10(t, s.URL())
	flagFormat = "yaml"
	out, err := captureStdoutCovCli10(t, func() error {
		return approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-y"})
	})
	if err != nil {
		t.Fatalf("yaml get: %v", err)
	}
	if !strings.Contains(out, "apr-y") || !strings.Contains(out, "status: pending") {
		t.Errorf("yaml output wrong: %q", out)
	}
}

func TestApprovalsListRunE_YAMLAndTransportError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"id": "apr-1", "status": "pending", "kind": "shell.exec"}}, "count": 1,
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "yaml"
	out, err := captureStdoutCovCli10(t, func() error {
		return approvalsListCmd.RunE(approvalsListCmd, nil)
	})
	if err != nil {
		t.Fatalf("yaml list: %v", err)
	}
	if !strings.Contains(out, "apr-1") {
		t.Errorf("yaml list missing row: %q", out)
	}

	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	if err := approvalsListCmd.RunE(approvalsListCmd, nil); err == nil {
		t.Error("expected transport error")
	}
}

func TestDecideApproval_TransportError(t *testing.T) {
	s := clitest.NewStubServer()
	s.Close()
	covSetupCli10(t, s.URL())
	if err := decideApproval(approvalsApproveCmd, "apr-1", "approved"); err == nil {
		t.Error("expected transport error")
	}
}

func TestApprovalsListRunE_StatusColorBranches(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "apr-a", "status": "approved", "kind": "shell.exec"},
			{"id": "apr-d", "status": "denied", "kind": "http.post"},
			{"id": "apr-t", "status": "timeout", "kind": "fs.write"},
		},
		"count": 3,
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return approvalsListCmd.RunE(approvalsListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"approved", "denied", "timeout", "apr-a", "apr-d", "apr-t"} {
		if !strings.Contains(out, want) {
			t.Errorf("status row missing %q:\n%s", want, out)
		}
	}
}

func TestApprovalsMalformedJSONResponses(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/approvals", clitest.TextResponse(200, `{broken`))
	s.OnGet("/api/v1/approvals/apr-1", clitest.TextResponse(200, `{broken`))
	s.OnPost("/api/v1/approvals/apr-1/decide", clitest.TextResponse(200, `{broken`))
	s.OnPost("/api/v1/approvals/reset-auto-tuning", clitest.TextResponse(200, `{broken`))
	covSetupCli10(t, s.URL())

	if err := approvalsListCmd.RunE(approvalsListCmd, nil); err == nil {
		t.Error("list: expected decode error")
	}
	if err := approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-1"}); err == nil {
		t.Error("get: expected decode error")
	}
	if err := decideApproval(approvalsApproveCmd, "apr-1", "approved"); err == nil {
		t.Error("decide: expected decode error")
	}
	if err := approvalsResetAutoTuningRunEForCov(); err == nil {
		t.Error("reset: expected decode error")
	}
}

// approvalsResetAutoTuningRunEForCov is a tiny indirection so the test
// above reads uniformly.
func approvalsResetAutoTuningRunEForCov() error {
	return approvalsResetAutoTuningCmd.RunE(approvalsResetAutoTuningCmd, []string{"shell.exec"})
}

func TestApprovalsGetRunE_TransportError(t *testing.T) {
	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	if err := approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr-1"}); err == nil {
		t.Error("expected transport error")
	}
	if err := approvalsResetAutoTuningRunEForCov(); err == nil {
		t.Error("reset: expected transport error")
	}
}

func TestApprovalsResetAutoTuningRunE_AuthGuards(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := approvalsResetAutoTuningCmd.RunE(approvalsResetAutoTuningCmd, []string{"t"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := approvalsResetAutoTuningCmd.RunE(approvalsResetAutoTuningCmd, []string{"t"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestApprovalsResetAutoTuningRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/approvals/reset-auto-tuning", clitest.ErrorResponse(403, "OWNER required"))
	covSetupCli10(t, s.URL())
	err := approvalsResetAutoTuningCmd.RunE(approvalsResetAutoTuningCmd, []string{"shell.exec"})
	if err == nil || !strings.Contains(err.Error(), "OWNER required") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}
