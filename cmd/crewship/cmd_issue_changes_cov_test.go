package main

// Coverage for `issue changes` (cmd_issue_extra.go, issueChangesCmd) — CLI
// parity for GET /api/v1/crews/{crewId}/git-diff. Fixture shaped from
// ProxyHandler.CrewGitDiff (internal/api/proxy.go), which passes the
// sidecar's IPC response through as-is (is_repo/files/diff/truncated).

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIssueChangesRunE_NotARepo(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/crews/"+covCrewID+"/git-diff", clitest.JSONResponse(200, map[string]any{"is_repo": false}))

	out, err := covCaptureStdout(t, func() error {
		return issueChangesCmd.RunE(issueChangesCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No git repository") {
		t.Errorf("output = %q", out)
	}
}

func TestIssueChangesRunE_NoChanges(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/crews/"+covCrewID+"/git-diff",
		clitest.JSONResponse(200, map[string]any{"is_repo": true, "files": []any{}}))

	out, err := covCaptureStdout(t, func() error {
		return issueChangesCmd.RunE(issueChangesCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No changes against the base branch.") {
		t.Errorf("output = %q", out)
	}
}

func TestIssueChangesRunE_FilesTable(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/crews/"+covCrewID+"/git-diff", clitest.JSONResponse(200, map[string]any{
		"is_repo": true,
		"files": []map[string]any{
			{"path": "cmd/crewship/cmd_issue_extra.go", "status": "modified", "additions": 12, "deletions": 3},
		},
		"diff": "--- a/x\n+++ b/x\n", "truncated": false,
	}))

	out, err := covCaptureStdout(t, func() error {
		return issueChangesCmd.RunE(issueChangesCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"modified", "cmd_issue_extra.go", "12", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIssueChangesRunE_PatchFlag(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/crews/"+covCrewID+"/git-diff", clitest.JSONResponse(200, map[string]any{
		"is_repo": true,
		"files":   []map[string]any{{"path": "x", "status": "modified", "additions": 1, "deletions": 1}},
		"diff":    "--- a/x\n+++ b/x\n@@ ...\n", "truncated": true,
	}))
	if err := issueChangesCmd.Flags().Set("patch", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issueChangesCmd.Flags().Set("patch", "false") })

	out, err := covCaptureStdout(t, func() error {
		return issueChangesCmd.RunE(issueChangesCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "--- a/x") {
		t.Errorf("--patch should print the raw diff:\n%s", out)
	}
	if !strings.Contains(out, "(diff truncated)") {
		t.Errorf("truncation must be surfaced under --patch:\n%s", out)
	}
}

// -f json must carry the truncation flag as a field rather than as prose
// appended to the patch body — a truncated diff that says so inside the
// patch text no longer applies cleanly.
func TestIssueChangesRunE_PatchJSONKeepsTruncationOutOfBody(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/crews/"+covCrewID+"/git-diff", clitest.JSONResponse(200, map[string]any{
		"is_repo": true,
		"files":   []map[string]any{{"path": "x", "status": "modified", "additions": 1, "deletions": 1}},
		"diff":    "--- a/x\n+++ b/x\n", "truncated": true,
	}))
	if err := issueChangesCmd.Flags().Set("patch", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issueChangesCmd.Flags().Set("patch", "false") })

	setFormatCov(t, "json")
	var runErr error
	out := covCaptureAll(t, func() {
		runErr = issueChangesCmd.RunE(issueChangesCmd, []string{covIssueIdent})
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	doc := covJSONBody(t, []byte(out))
	if doc["truncated"] != true {
		t.Errorf("truncated field missing/false in json: %v", doc)
	}
	diff, _ := doc["diff"].(string)
	if strings.Contains(diff, "truncated") {
		t.Errorf("diff body must not carry the truncation prose: %q", diff)
	}
}
