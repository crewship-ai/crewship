package main

// Coverage for `issue detach` (cmd_issue_attachments.go, issueDetachCmd) —
// the one attachment subcommand left untested; attach/attachments/attachment
// already have real coverage in cmd_issue_attachments_test.go. Also closes
// issueBulkCmd (cmd_issue_extra.go): its "update" child is well covered
// (cmd_issue_extra_cov_test.go) but the group var's own name never appears.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIssueDetachRunE_Happy(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	attID := "catt0123456789abcdefghij"
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/attachments/" + attID
	s.OnDelete(path, clitest.JSONResponse(200, map[string]string{}))

	out, err := covCaptureStdout(t, func() error {
		return issueDetachCmd.RunE(issueDetachCmd, []string{covIssueIdent, attID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Attachment removed.") {
		t.Errorf("output = %q", out)
	}
	if n := len(s.CallsFor("DELETE", path)); n != 1 {
		t.Errorf("DELETE calls = %d, want 1", n)
	}
}

func TestIssueDetachRunE_NotFound(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/attachments/ghost"
	s.OnDelete(path, clitest.ErrorResponse(404, "attachment not found"))

	_, err := covCaptureStdout(t, func() error {
		return issueDetachCmd.RunE(issueDetachCmd, []string{covIssueIdent, "ghost"})
	})
	if err == nil || !strings.Contains(err.Error(), "attachment not found") {
		t.Errorf("want not-found error surfaced, got %v", err)
	}
}

func TestIssueDetachRunE_IssueNotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/issues/GHOST-1", clitest.ErrorResponse(404, "issue not found"))

	_, err := covCaptureStdout(t, func() error {
		return issueDetachCmd.RunE(issueDetachCmd, []string{"GHOST-1", "att-1"})
	})
	if err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("want issue-not-found error, got %v", err)
	}
}

// ─── issueBulkCmd group ──────────────────────────────────────────────────

func TestIssueBulkCmd_HasUpdateChild(t *testing.T) {
	have := map[string]bool{}
	for _, c := range issueBulkCmd.Commands() {
		have[c.Name()] = true
	}
	if !have["update"] {
		t.Error("issueBulkCmd missing subcommand \"update\"")
	}
	if issueBulkCmd.Use != "bulk" {
		t.Errorf("issueBulkCmd.Use = %q, want \"bulk\"", issueBulkCmd.Use)
	}
}
