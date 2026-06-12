package main

// Coverage tests for cmd_issue_extra.go — comments/relations/routine-binding/
// subtasks/activity/bulk subcommands. Serial; shared cov* helpers live in
// cmd_skill_cov_test.go.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covIssueIdent = "ENG-42"

// covStubIssue registers the GET /api/v1/issues/{ident} lookup that
// fetchIssue performs before every mutation.
func covStubIssue(s *clitest.StubServer) {
	s.OnGet("/api/v1/issues/"+covIssueIdent, clitest.JSONResponse(200, map[string]any{
		"id": "cissue0123456789abcdefgh", "crew_id": covCrewID,
		"identifier": covIssueIdent, "title": "Fix the thing",
		"status": "IN_PROGRESS", "priority": "high",
	}))
}

// ─── resolveRoutineID ────────────────────────────────────────────────────

func TestResolveRoutineIDCov_Happy(t *testing.T) {
	s := covSetup(t)
	path := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/pipelines/nightly-triage"
	s.OnGet(path, clitest.JSONResponse(200, map[string]string{"id": "cpipe0123456789abcdefghi"}))

	got, err := resolveRoutineID(newAPIClient(), "nightly-triage")
	if err != nil {
		t.Fatalf("resolveRoutineID: %v", err)
	}
	if got != "cpipe0123456789abcdefghi" {
		t.Errorf("got %q", got)
	}
}

func TestResolveRoutineIDCov_NotFound(t *testing.T) {
	s := covSetup(t)
	path := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/pipelines/ghost"
	s.OnGet(path, clitest.ErrorResponse(404, "pipeline not found"))
	_, err := resolveRoutineID(newAPIClient(), "ghost")
	if err == nil || !strings.Contains(err.Error(), `routine "ghost"`) {
		t.Errorf("want wrapped routine error; got %v", err)
	}
}

func TestResolveRoutineIDCov_EmptyID(t *testing.T) {
	s := covSetup(t)
	path := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/pipelines/blank"
	s.OnGet(path, clitest.JSONResponse(200, map[string]string{}))
	_, err := resolveRoutineID(newAPIClient(), "blank")
	if err == nil || !strings.Contains(err.Error(), "has no id in response") {
		t.Errorf("want empty-id error; got %v", err)
	}
}

// ─── issue comments ──────────────────────────────────────────────────────

func TestIssueCommentsRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/comments"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{
		{"id": "ccomment0123456789abcdef", "author_type": "agent", "author_name": "Viktor",
			"body": "line one\nline two", "created_at": "2026-06-01T10:00:00Z"},
	}))
	out, err := covCaptureStdout(t, func() error {
		return issueCommentsCmd.RunE(issueCommentsCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Viktor") {
		t.Errorf("table missing author:\n%s", out)
	}
	// Newlines in bodies must be flattened for the table row.
	if !strings.Contains(out, "line one line two") {
		t.Errorf("body newlines not flattened:\n%s", out)
	}
}

// ─── issue relate / relations / unrelate ─────────────────────────────────

func TestIssueRelateRunECov_InvalidType(t *testing.T) {
	covSetup(t)
	covSetFlag(t, issueRelateCmd, "type", "nonsense")
	err := issueRelateCmd.RunE(issueRelateCmd, []string{covIssueIdent, "ENG-43"})
	if err == nil || !strings.Contains(err.Error(), "--type must be one of") {
		t.Errorf("want type validation error; got %v", err)
	}
}

func TestIssueRelateRunECov_HyphenAliasNormalized(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/relations"
	s.OnPost(path, clitest.JSONResponse(201, map[string]string{"id": "crel"}))
	covSetFlag(t, issueRelateCmd, "type", "duplicate-of")

	if _, err := covCaptureStdout(t, func() error {
		return issueRelateCmd.RunE(issueRelateCmd, []string{covIssueIdent, "ENG-43"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covJSONBody(t, s.CallsFor("POST", path)[0].Body)
	if body["relation_type"] != "duplicate_of" {
		t.Errorf("relation_type = %v, want duplicate_of (hyphen normalized)", body["relation_type"])
	}
	if body["target_identifier"] != "ENG-43" {
		t.Errorf("target_identifier = %v", body["target_identifier"])
	}
}

func TestIssueRelationsRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/relations"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{
		{"id": "crelation0123456789abcde", "relation_type": "blocks",
			"target_identifier": "ENG-43", "target_title": "Downstream work",
			"target_status": "TODO", "created_at": "2026-06-01"},
	}))
	out, err := covCaptureStdout(t, func() error {
		return issueRelationsCmd.RunE(issueRelationsCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"blocks", "ENG-43", "Downstream work"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIssueUnrelateRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnDelete("/api/v1/relations/rel-1", clitest.JSONResponse(200, map[string]string{}))
	if _, err := covCaptureStdout(t, func() error {
		return issueUnrelateCmd.RunE(issueUnrelateCmd, []string{"rel-1"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/relations/rel-1")); n != 1 {
		t.Errorf("DELETE calls = %d, want 1", n)
	}
}

// ─── routine bind / unbind ───────────────────────────────────────────────

func TestIssueBindRoutineRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	routineID := "cpipe0123456789abcdefghi"
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli1+"/pipelines/nightly",
		clitest.JSONResponse(200, map[string]string{"id": routineID}))
	patchPath := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent
	s.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{"id": "x"}))

	if _, err := covCaptureStdout(t, func() error {
		return issueBindRoutineCmd.RunE(issueBindRoutineCmd, []string{covIssueIdent, "nightly"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", patchPath)[0].Body)
	if body["routine_id"] != routineID {
		t.Errorf("routine_id = %v, want pipeline CUID (not slug)", body["routine_id"])
	}
}

func TestIssueUnbindRoutineRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	patchPath := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent
	s.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{"id": "x"}))

	if _, err := covCaptureStdout(t, func() error {
		return issueUnbindRoutineCmd.RunE(issueUnbindRoutineCmd, []string{covIssueIdent})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", patchPath)[0].Body)
	if v, ok := body["routine_id"]; !ok || v != "" {
		t.Errorf("routine_id = %v, want explicit empty string (server normalizes to NULL)", v)
	}
}

// ─── subtasks / activity ─────────────────────────────────────────────────

func TestIssueSubtasksRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/subtasks"
	assignee := "Nela"
	ident := "ENG-50"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{
		{"id": "csub0123456789abcdefghij", "identifier": ident, "title": "Child task",
			"status": "TODO", "priority": "low", "assignee_name": assignee},
		{"id": "csub20123456789abcdefghi", "identifier": nil, "title": "No ident child",
			"status": "DONE", "priority": "", "assignee_name": nil},
	}))
	out, err := covCaptureStdout(t, func() error {
		return issueSubtasksCmd.RunE(issueSubtasksCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"ENG-50", "Child task", "Nela", "Low"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIssueActivityRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/activity"
	actor := "Viktor"
	details := "status TODO → IN_PROGRESS"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{
		{"id": "cev1", "actor_type": "agent", "actor_name": actor,
			"action": "status_changed", "details": details, "created_at": "2026-06-01T10:00:00Z"},
		{"id": "cev2", "actor_type": "system", "actor_name": nil,
			"action": "created", "details": nil, "created_at": "2026-06-01T09:00:00Z"},
	}))
	out, err := covCaptureStdout(t, func() error {
		return issueActivityCmd.RunE(issueActivityCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"agent/Viktor", "status_changed", "system/-"} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q:\n%s", want, out)
		}
	}
}

// ─── issue bulk update ───────────────────────────────────────────────────

func TestIssueBulkUpdateRunECov_Validation(t *testing.T) {
	cases := []struct {
		name    string
		ids     string
		wantErr string
		extra   func(t *testing.T)
	}{
		{"missing ids", "", "--ids is required", nil},
		{"only separators", " , ,", "--ids is empty after trimming", nil},
		{"over cap", strings.Repeat("x,", 100) + "x", "server caps bulk update at 100", nil},
		{"no updates", "ms_a,ms_b", "at least one of", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covSetup(t)
			if tc.ids != "" {
				covSetFlag(t, issueBulkUpdateCmd, "ids", tc.ids)
			}
			err := issueBulkUpdateCmd.RunE(issueBulkUpdateCmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want %q; got %v", tc.wantErr, err)
			}
		})
	}
}

func TestIssueBulkUpdateRunECov_Happy(t *testing.T) {
	s := covSetup(t)
	s.OnPatch("/api/v1/issues/bulk", clitest.JSONResponse(200, map[string]int{"updated": 2}))
	covSetFlag(t, issueBulkUpdateCmd, "ids", "ms_a, ms_b,")
	covSetFlag(t, issueBulkUpdateCmd, "status", "DONE")
	covSetFlag(t, issueBulkUpdateCmd, "priority", "high")
	covSetFlag(t, issueBulkUpdateCmd, "assignee", "cagent0123456789abcdefgh")
	covSetFlag(t, issueBulkUpdateCmd, "labels", "lbl_bug, lbl_p0")

	out, err := covCaptureStdout(t, func() error {
		return issueBulkUpdateCmd.RunE(issueBulkUpdateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Bulk update applied to 2/2 issue(s).") {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/issues/bulk")[0].Body)
	ids, _ := body["ids"].([]any)
	if len(ids) != 2 || ids[0] != "ms_a" || ids[1] != "ms_b" {
		t.Errorf("ids = %v, want trimmed [ms_a ms_b]", body["ids"])
	}
	updates, _ := body["updates"].(map[string]any)
	if updates["status"] != "DONE" || updates["priority"] != "high" {
		t.Errorf("updates = %v", updates)
	}
	if updates["assignee_id"] != "cagent0123456789abcdefgh" || updates["assignee_type"] != "agent" {
		t.Errorf("assignee fields = %v", updates)
	}
	labels, _ := updates["labels"].([]any)
	if len(labels) != 2 || labels[0] != "lbl_bug" || labels[1] != "lbl_p0" {
		t.Errorf("labels = %v, want trimmed pair", updates["labels"])
	}
}

func TestIssueBulkUpdateRunECov_ClearAssigneeAndLabels(t *testing.T) {
	s := covSetup(t)
	s.OnPatch("/api/v1/issues/bulk", clitest.JSONResponse(200, map[string]int{"updated": 1}))
	covSetFlag(t, issueBulkUpdateCmd, "ids", "ms_a")
	covSetFlag(t, issueBulkUpdateCmd, "assignee", "")
	covSetFlag(t, issueBulkUpdateCmd, "labels", "")
	covSetFlag(t, issueBulkUpdateCmd, "project", "")

	if _, err := covCaptureStdout(t, func() error {
		return issueBulkUpdateCmd.RunE(issueBulkUpdateCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	updates, _ := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/issues/bulk")[0].Body)["updates"].(map[string]any)
	if updates["assignee_id"] != "" || updates["assignee_type"] != "" {
		t.Errorf("clearing assignee must send empty strings; got %v", updates)
	}
	labels, ok := updates["labels"].([]any)
	if !ok || len(labels) != 0 {
		t.Errorf("clearing labels must send empty array; got %v", updates["labels"])
	}
	if updates["project_id"] != "" {
		t.Errorf("clearing project must send empty string; got %v", updates["project_id"])
	}
}
