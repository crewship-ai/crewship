package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// stubIssueDirectory wires the crew + agent lists used by slug
// resolution plus the issue fetch used by update/delete.
func stubIssueDirectory(stub *clitest.StubServer) {
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli4, "slug": "engineering"},
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli4, "slug": "viktor"},
	}))
	stub.OnGet("/api/v1/issues/ENG-7", clitest.JSONResponse(200, map[string]any{
		"id": "ciss0123456789abcdefghij", "crew_id": covCrewIDCli4,
		"identifier": "ENG-7", "title": "Fix flaky test",
		"status": "in_progress", "priority": "high",
		"mission_type": "ISSUE", "created_at": "2026-06-01T00:00:00Z", "updated_at": "2026-06-01T00:00:00Z",
	}))
}

func declareIssueCreateFlags(c *cobra.Command) {
	for _, f := range []string{"crew", "title", "description", "priority", "assignee", "assignee-type", "labels", "due-date", "project-id", "milestone-id", "parent-issue-id", "routine-id"} {
		c.Flags().String(f, "", "")
	}
	c.Flags().Int("estimate", 0, "")
	c.Flags().Float64("sort-order", 0, "")
}

func TestIssueCreateRunE_CrewAndTitleRequired(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(issueCreateCmd, declareIssueCreateFlags)
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Fatalf("want crew-required, got %v", err)
	}
	covSetFlagsCli4(t, c, map[string]string{"crew": "engineering"})
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "--title is required") {
		t.Fatalf("want title-required, got %v", err)
	}
}

func TestIssueCreateRunE_HappyPath_FullBody(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli4+"/issues", clitest.JSONResponse(201, map[string]any{
		"id": "ciss0123456789abcdefghij", "crew_id": covCrewIDCli4,
		"identifier": "ENG-8", "title": "Ship hardening",
		"status": "backlog", "priority": "high",
		"mission_type": "ISSUE", "created_at": "x", "updated_at": "x",
	}))

	c := covFreshCmd(issueCreateCmd, declareIssueCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"crew":            "engineering",
		"title":           "Ship hardening",
		"description":     "do the work",
		"priority":        "high",
		"assignee":        "viktor",
		"labels":          "bug,infra",
		"due-date":        "2026-07-01",
		"project-id":      "p1",
		"milestone-id":    "m1",
		"parent-issue-id": "ENG-1",
		"estimate":        "3",
		"sort-order":      "1.5",
		"routine-id":      "rt-1",
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Created issue ENG-8: Ship hardening") {
		t.Errorf("success line missing: %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli4+"/issues")
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["title"] != "Ship hardening" || body["description"] != "do the work" || body["priority"] != "high" {
		t.Errorf("core fields wrong: %v", body)
	}
	if body["assignee_id"] != covAgentIDCli4 || body["assignee_type"] != "agent" {
		t.Errorf("assignee resolution wrong: %v", body)
	}
	labels, ok := body["labels"].([]any)
	if !ok || len(labels) != 2 || labels[0] != "bug" || labels[1] != "infra" {
		t.Errorf("labels split wrong: %v", body["labels"])
	}
	if body["due_date"] != "2026-07-01" || body["project_id"] != "p1" || body["milestone_id"] != "m1" ||
		body["parent_issue_id"] != "ENG-1" || body["routine_id"] != "rt-1" {
		t.Errorf("association fields wrong: %v", body)
	}
	if body["estimate"] != float64(3) || body["sort_order"] != 1.5 {
		t.Errorf("estimate/sort_order wrong: %v", body)
	}
}

func TestIssueCreateRunE_UnsupportedAssigneeType(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	c := covFreshCmd(issueCreateCmd, declareIssueCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"crew": "engineering", "title": "x", "assignee": "someone", "assignee-type": "user",
	})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), `--assignee-type "user" is not supported`) {
		t.Fatalf("want unsupported assignee-type, got %v", err)
	}
}

func declareIssueUpdateFlags(c *cobra.Command) {
	for _, f := range []string{"title", "description", "status", "priority", "assignee", "assignee-type", "due-date", "project-id", "milestone-id", "parent-issue-id", "routine-id"} {
		c.Flags().String(f, "", "")
	}
	c.Flags().Int("estimate", 0, "")
	c.Flags().Float64("sort-order", 0, "")
}

func TestIssueUpdateRunE_NoFields(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	err := c.RunE(c, []string{"ENG-7"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Fatalf("want no-fields error, got %v", err)
	}
}

func TestIssueUpdateRunE_HappyPath_PatchPathAndBody(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	patchPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{"id": "ciss"}))

	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"status":     "done",
		"title":      "Fixed it",
		"assignee":   "", // explicit empty → clear assignee
		"routine-id": "",
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"ENG-7"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Issue ENG-7 updated.") {
		t.Errorf("success line missing: %q", out)
	}

	calls := stub.CallsFor("PATCH", patchPath)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "done" || body["title"] != "Fixed it" {
		t.Errorf("fields wrong: %v", body)
	}
	// Cleared assignee must be explicit null for both columns.
	if v, present := body["assignee_id"]; !present || v != nil {
		t.Errorf("assignee_id = %v (present=%v), want explicit null", v, present)
	}
	if v, present := body["assignee_type"]; !present || v != nil {
		t.Errorf("assignee_type = %v (present=%v), want explicit null", v, present)
	}
	if v, present := body["routine_id"]; !present || v != "" {
		t.Errorf("routine_id = %v (present=%v), want empty string", v, present)
	}
}

func TestIssueUpdateRunE_ReassignResolvesAgent(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	patchPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{"id": "ciss"}))

	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"assignee": "viktor"})
	if _, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"ENG-7"}) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", patchPath)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d", len(calls))
	}
	var body map[string]any
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["assignee_id"] != covAgentIDCli4 || body["assignee_type"] != "agent" {
		t.Errorf("reassign body wrong: %v", body)
	}
}

func TestIssueUpdateRunE_AssigneeTypeAlone(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)

	// Non-"agent" value errors out.
	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"assignee-type": "user"})
	err := c.RunE(c, []string{"ENG-7"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("want unsupported error, got %v", err)
	}

	// Blank value clears the column.
	patchPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{"id": "ciss"}))
	c2 := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c2, map[string]string{"assignee-type": " "})
	if _, err := covCaptureStdoutCli4(t, func() error { return c2.RunE(c2, []string{"ENG-7"}) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", patchPath)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d", len(calls))
	}
	var body map[string]any
	_ = json.Unmarshal(calls[0].Body, &body)
	if v, present := body["assignee_type"]; !present || v != nil {
		t.Errorf("assignee_type = %v (present=%v), want explicit null", v, present)
	}
}

func TestIssueUpdateRunE_FetchIssueErrorSurfaces(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/issues/ENG-404", clitest.ErrorResponse(404, "issue not found"))
	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"status": "done"})
	err := c.RunE(c, []string{"ENG-404"})
	if err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Fatalf("want fetch error, got %v", err)
	}
}

func TestIssueDeleteRunE_YesDeletesViaCrewScopedPath(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	delPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnDelete(delPath, clitest.EmptyResponse(204))

	c := covFreshCmd(issueDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"ENG-7"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Issue ENG-7 deleted.") {
		t.Errorf("success line missing: %q", out)
	}
	if got := len(stub.CallsFor("DELETE", delPath)); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestIssueDeleteRunE_AbortBeforeFetch(t *testing.T) {
	stub := covSetupCli4(t)
	c := covFreshCmd(issueDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	err := covWithStdinCli4(t, "n\n", func() error { return c.RunE(c, []string{"ENG-7"}) })
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("want aborted, got %v", err)
	}
	if got := len(stub.Calls()); got != 0 {
		t.Errorf("abort must happen before any API call, got %d calls", got)
	}
}

// ─── remaining error branches ────────────────────────────────────────

func TestIssueLifecycleGates_AuthAndWorkspace(t *testing.T) {
	builders := map[string]func() *cobra.Command{
		"create": func() *cobra.Command { return covFreshCmd(issueCreateCmd, declareIssueCreateFlags) },
		"update": func() *cobra.Command { return covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags) },
		"delete": func() *cobra.Command {
			return covFreshCmd(issueDeleteCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") })
		},
	}
	for name, build := range builders {
		t.Run(name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			c := build()
			if err := c.RunE(c, []string{"ENG-7"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			c := build()
			if err := c.RunE(c, []string{"ENG-7"}); err == nil || !strings.Contains(err.Error(), "no workspace set") {
				t.Errorf("want workspace error, got %v", err)
			}
		})
	}
}

func TestIssueCreateRunE_CrewResolveAndAssigneeResolveErrors(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	c := covFreshCmd(issueCreateCmd, declareIssueCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"crew": "ghost-crew", "title": "x"})
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "crew not found: ghost-crew") {
		t.Fatalf("want crew resolve error, got %v", err)
	}

	// Crew resolves but the assignee does not.
	stubIssueDirectory(stub)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	c2 := covFreshCmd(issueCreateCmd, declareIssueCreateFlags)
	covSetFlagsCli4(t, c2, map[string]string{"crew": "engineering", "title": "x", "assignee": "ghost"})
	err := c2.RunE(c2, nil)
	if err == nil || !strings.Contains(err.Error(), `cannot resolve assignee "ghost"`) {
		t.Fatalf("want assignee resolve error, got %v", err)
	}
}

func TestIssueCreateRunE_ServerAndDecodeErrors(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	postPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues"

	stub.OnPost(postPath, clitest.ErrorResponse(400, "title too long"))
	c := covFreshCmd(issueCreateCmd, declareIssueCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"crew": "engineering", "title": "x"})
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "title too long") {
		t.Fatalf("want server error, got %v", err)
	}

	stub.OnPost(postPath, clitest.TextResponse(201, "not-json"))
	if err := c.RunE(c, nil); err == nil {
		t.Fatal("want decode error for malformed create response")
	}
}

func TestIssueUpdateRunE_AllScalarFlags(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	patchPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{"id": "ciss"}))

	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"description":     "new desc",
		"priority":        "urgent",
		"due-date":        "2026-08-01",
		"project-id":      "p1",
		"milestone-id":    "m1",
		"parent-issue-id": "ENG-1",
		"estimate":        "5",
		"sort-order":      "2.5",
		"assignee-type":   "agent", // alone, valid value → forwarded as-is
	})
	if _, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"ENG-7"}) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", patchPath)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"description": "new desc", "priority": "urgent", "due_date": "2026-08-01",
		"project_id": "p1", "milestone_id": "m1", "parent_issue_id": "ENG-1",
		"estimate": float64(5), "sort_order": 2.5, "assignee_type": "agent",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
}

func TestIssueUpdateRunE_AssigneeResolveErrorAndPatchError(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	c := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"assignee": "ghost"})
	err := c.RunE(c, []string{"ENG-7"})
	if err == nil || !strings.Contains(err.Error(), `cannot resolve assignee "ghost"`) {
		t.Fatalf("want assignee resolve error, got %v", err)
	}

	patchPath := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnPatch(patchPath, clitest.ErrorResponse(409, "issue locked"))
	c2 := covFreshCmd(issueUpdateCmd, declareIssueUpdateFlags)
	covSetFlagsCli4(t, c2, map[string]string{"status": "done"})
	if err := c2.RunE(c2, []string{"ENG-7"}); err == nil || !strings.Contains(err.Error(), "issue locked") {
		t.Fatalf("want patch server error, got %v", err)
	}
}

func TestIssueDeleteRunE_FetchAndDeleteErrors(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/issues/ENG-404", clitest.ErrorResponse(404, "issue not found"))

	c := covFreshCmd(issueDeleteCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") })
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})
	if err := c.RunE(c, []string{"ENG-404"}); err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Fatalf("want fetch error, got %v", err)
	}

	stubIssueDirectory(stub)
	stub.OnDelete("/api/v1/crews/"+covCrewIDCli4+"/issues/ENG-7", clitest.ErrorResponse(403, "delete denied"))
	if err := c.RunE(c, []string{"ENG-7"}); err == nil || !strings.Contains(err.Error(), "delete denied") {
		t.Fatalf("want delete server error, got %v", err)
	}
}
