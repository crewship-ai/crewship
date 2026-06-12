package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestProjectCmdStructure(t *testing.T) {
	if projectCmd.Use != "project" {
		t.Errorf("Use = %q, want project", projectCmd.Use)
	}
	have := map[string]bool{}
	for _, sub := range projectCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "get", "update", "delete", "stats"} {
		if !have[want] {
			t.Errorf("project missing subcommand %q; have %v", want, have)
		}
	}
}

func TestProjectListRunE_TableRows(t *testing.T) {
	stub := covSetupCli4(t)
	target := "2026-07-01"
	stub.OnGet("/api/v1/projects", clitest.JSONResponse(200, []map[string]any{
		{"id": "p1", "name": "Hardening", "slug": "hardening", "status": "in_progress",
			"priority": "high", "health": "on_track", "issue_count": 10, "done_count": 4,
			"progress": 40, "target_date": target, "created_at": "2026-05-01T00:00:00Z"},
		{"id": "p2", "name": "Backlog", "slug": "backlog", "status": "planned",
			"priority": "none", "health": "on_track", "issue_count": 0, "done_count": 0,
			"progress": 0, "target_date": nil, "created_at": "2026-05-02T00:00:00Z"},
	}))

	c := covFreshCmd(projectListCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Hardening") || !strings.Contains(out, "4/10") || !strings.Contains(out, "40%") || !strings.Contains(out, target) {
		t.Errorf("project row missing fields: %q", out)
	}
	// nil target renders "-"
	if !strings.Contains(out, "Backlog") {
		t.Errorf("second row missing: %q", out)
	}
}

func TestProjectListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	c := covFreshCmd(projectListCmd, nil)
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want not-logged-in, got %v", err)
	}
}

func declareProjectCreateFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("color", "", "")
	c.Flags().String("status", "planned", "")
	c.Flags().String("priority", "none", "")
	c.Flags().String("icon", "", "")
	c.Flags().String("target-date", "", "")
	c.Flags().String("lead-id", "", "")
	c.Flags().String("lead-type", "", "")
	c.Flags().String("start-date", "", "")
}

func TestProjectCreateRunE_NameRequired(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(projectCreateCmd, declareProjectCreateFlags)
	// Defaults carry status/priority but name is empty → must fail first.
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("want name-required, got %v", err)
	}
}

func TestProjectCreateRunE_HappyPath_FullBody(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/projects", clitest.JSONResponse(201, map[string]string{
		"id": "p9", "name": "Release 1.0", "slug": "release-1-0",
	}))

	c := covFreshCmd(projectCreateCmd, declareProjectCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"name":        "Release 1.0",
		"description": "ship it",
		"color":       "#FF0000",
		"status":      "in_progress",
		"priority":    "urgent",
		"icon":        "rocket",
		"target-date": "2026-09-01",
		"lead-id":     "u-lead",
		"lead-type":   "user",
		"start-date":  "2026-06-15",
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Created project: Release 1.0 (release-1-0)") {
		t.Errorf("success line missing: %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/projects")
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"name": "Release 1.0", "description": "ship it", "color": "#FF0000",
		"status": "in_progress", "priority": "urgent", "icon": "rocket",
		"target_date": "2026-09-01", "lead_id": "u-lead", "lead_type": "user",
		"start_date": "2026-06-15",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
}

func TestProjectGetRunE_DetailPairs(t *testing.T) {
	stub := covSetupCli4(t)
	lead := "Viktor"
	desc := "the big one"
	stub.OnGet("/api/v1/projects/hardening", clitest.JSONResponse(200, map[string]any{
		"id": "p1", "name": "Hardening", "slug": "hardening", "status": "in_progress",
		"priority": "high", "health": "at_risk", "lead_name": lead, "description": desc,
		"issue_count": 12, "done_count": 3, "progress": 25, "target_date": nil,
		"created_at": "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(projectGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"hardening"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Hardening", "at_risk", "Viktor", "12 total, 3 done", "25%", "the big one"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail output missing %q: %q", want, out)
		}
	}
}

func declareProjectUpdateFlags(c *cobra.Command) {
	for _, f := range []string{"name", "description", "icon", "color", "status", "priority", "health", "lead-id", "lead-type", "start-date", "target-date"} {
		c.Flags().String(f, "", "")
	}
}

func TestProjectUpdateRunE_NoFields(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(projectUpdateCmd, declareProjectUpdateFlags)
	if err := c.RunE(c, []string{"p1"}); err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Fatalf("want no-fields error, got %v", err)
	}
}

func TestProjectUpdateRunE_OnlyChangedFlagsSent(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPatch("/api/v1/projects/p1", clitest.JSONResponse(200, map[string]string{"id": "p1"}))

	c := covFreshCmd(projectUpdateCmd, declareProjectUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"health":      "off_track",
		"target-date": "", // explicit empty clears the column server-side
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"p1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Project p1 updated.") {
		t.Errorf("success line missing: %q", out)
	}

	calls := stub.CallsFor("PATCH", "/api/v1/projects/p1")
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body) != 2 {
		t.Errorf("body should carry exactly the 2 changed flags, got %v", body)
	}
	if body["health"] != "off_track" {
		t.Errorf("health = %v", body["health"])
	}
	if v, present := body["target_date"]; !present || v != "" {
		t.Errorf("target_date should be explicit empty string, got %v (present=%v)", v, present)
	}
	if _, leaked := body["name"]; leaked {
		t.Errorf("unchanged flag leaked into body: %v", body)
	}
}

func TestProjectDeleteRunE_YesDeletes(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnDelete("/api/v1/projects/p1", clitest.EmptyResponse(204))

	c := covFreshCmd(projectDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"p1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Project p1 deleted.") {
		t.Errorf("success line missing: %q", out)
	}
	if got := len(stub.CallsFor("DELETE", "/api/v1/projects/p1")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestProjectDeleteRunE_AbortViaStdin(t *testing.T) {
	stub := covSetupCli4(t)
	c := covFreshCmd(projectDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	err := covWithStdinCli4(t, "no\n", func() error { return c.RunE(c, []string{"p1"}) })
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("want aborted, got %v", err)
	}
	if got := len(stub.Calls()); got != 0 {
		t.Errorf("no API calls expected after abort, got %d", got)
	}
}

func projectStatsPayload() map[string]any {
	return map[string]any{
		"total_issues":     8,
		"completed_issues": 2,
		"by_status":        map[string]int{"in_progress": 3, "done": 2},
		"by_assignee": []map[string]any{
			{"agent_name": "viktor", "total": 5, "completed": 2},
		},
		"by_label": []map[string]any{
			{"label_name": "bug", "color": "#f00", "count": 4},
		},
		"crews": []string{"engineering"},
	}
}

func TestProjectStatsRunE_TableBreakdown(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/projects/p1/stats", clitest.JSONResponse(200, projectStatsPayload()))

	c := covFreshCmd(projectStatsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"p1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Issues: 8 total / 2 completed (25%)") {
		t.Errorf("summary line missing: %q", out)
	}
	for _, want := range []string{"By status:", "in_progress", "By assignee:", "viktor", "5 total / 2 done", "By label:", "bug", "Crews: [engineering]"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats output missing %q: %q", want, out)
		}
	}
}

func TestProjectStatsRunE_JSONFormat(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/projects/p1/stats", clitest.JSONResponse(200, projectStatsPayload()))
	cliCfg.Format = "json"

	c := covFreshCmd(projectStatsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"p1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var decoded struct {
		TotalIssues int `json:"total_issues"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("json mode output not parseable: %v\n%q", err, out)
	}
	if decoded.TotalIssues != 8 {
		t.Errorf("total_issues = %d, want 8", decoded.TotalIssues)
	}
}

func TestProjectStatsRunE_ServerError(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/projects/p1/stats", clitest.ErrorResponse(404, "project not found"))
	c := covFreshCmd(projectStatsCmd, nil)
	err := c.RunE(c, []string{"p1"})
	if err == nil || !strings.Contains(err.Error(), "project not found") {
		t.Fatalf("want 404 surfaced, got %v", err)
	}
}

// ─── remaining error branches ────────────────────────────────────────

func covProjectCmds() map[string]*cobra.Command {
	return map[string]*cobra.Command{
		"list":   covFreshCmd(projectListCmd, nil),
		"create": covFreshCmd(projectCreateCmd, declareProjectCreateFlags),
		"get":    covFreshCmd(projectGetCmd, nil),
		"update": covFreshCmd(projectUpdateCmd, declareProjectUpdateFlags),
		"delete": covFreshCmd(projectDeleteCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") }),
		"stats":  covFreshCmd(projectStatsCmd, nil),
	}
}

func TestProjectGates_AuthAndWorkspace(t *testing.T) {
	for name, c := range covProjectCmds() {
		t.Run(name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := c.RunE(c, []string{"p1"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			if err := c.RunE(c, []string{"p1"}); err == nil || !strings.Contains(err.Error(), "no workspace set") {
				t.Errorf("want workspace error, got %v", err)
			}
		})
	}
}

func TestProject_TransportErrors(t *testing.T) {
	cmds := covProjectCmds()
	covSetFlagsCli4(t, cmds["create"], map[string]string{"name": "X"})
	covSetFlagsCli4(t, cmds["update"], map[string]string{"status": "paused"})
	covSetFlagsCli4(t, cmds["delete"], map[string]string{"yes": "true"})
	for name, c := range cmds {
		t.Run(name, func(t *testing.T) {
			covDeadServerCli4(t)
			if err := c.RunE(c, []string{"p1"}); err == nil {
				t.Error("want transport error against dead server")
			}
		})
	}
}

func TestProject_ServerErrors(t *testing.T) {
	endpoints := map[string]struct{ method, path string }{
		"list":   {"GET", "/api/v1/projects"},
		"create": {"POST", "/api/v1/projects"},
		"get":    {"GET", "/api/v1/projects/p1"},
		"update": {"PATCH", "/api/v1/projects/p1"},
		"delete": {"DELETE", "/api/v1/projects/p1"},
	}
	cmds := covProjectCmds()
	covSetFlagsCli4(t, cmds["create"], map[string]string{"name": "X"})
	covSetFlagsCli4(t, cmds["update"], map[string]string{"status": "paused"})
	covSetFlagsCli4(t, cmds["delete"], map[string]string{"yes": "true"})
	for name, ep := range endpoints {
		t.Run(name, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.On(ep.method, ep.path, clitest.ErrorResponse(500, "projects exploded"))
			c := cmds[name]
			if err := c.RunE(c, []string{"p1"}); err == nil || !strings.Contains(err.Error(), "projects exploded") {
				t.Errorf("want server error surfaced, got %v", err)
			}
		})
	}
}

func TestProject_MalformedResponses(t *testing.T) {
	cmds := covProjectCmds()
	covSetFlagsCli4(t, cmds["create"], map[string]string{"name": "X"})

	t.Run("list", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnGet("/api/v1/projects", clitest.JSONResponse(200, map[string]string{"not": "array"}))
		if err := cmds["list"].RunE(cmds["list"], nil); err == nil {
			t.Error("want decode error")
		}
	})
	t.Run("create", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnPost("/api/v1/projects", clitest.TextResponse(201, "not-json"))
		if err := cmds["create"].RunE(cmds["create"], nil); err == nil {
			t.Error("want decode error")
		}
	})
	t.Run("get", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnGet("/api/v1/projects/p1", clitest.TextResponse(200, "not-json"))
		if err := cmds["get"].RunE(cmds["get"], []string{"p1"}); err == nil {
			t.Error("want decode error")
		}
	})
	t.Run("stats", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnGet("/api/v1/projects/p1/stats", clitest.TextResponse(200, "not-json"))
		if err := cmds["stats"].RunE(cmds["stats"], []string{"p1"}); err == nil {
			t.Error("want decode error")
		}
	})
}

func TestProjectGetRunE_TargetDateSet(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/projects/p2", clitest.JSONResponse(200, map[string]any{
		"id": "p2", "name": "Dated", "slug": "dated", "status": "planned",
		"priority": "low", "health": "on_track", "target_date": "2026-12-24",
		"issue_count": 1, "done_count": 0, "progress": 0,
		"created_at": "2026-05-01T00:00:00Z",
	}))
	c := covFreshCmd(projectGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"p2"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "2026-12-24") {
		t.Errorf("target date missing: %q", out)
	}
}

func TestProjectStatsRunE_YAMLFormat(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/projects/p1/stats", clitest.JSONResponse(200, projectStatsPayload()))
	cliCfg.Format = "yaml"
	c := covFreshCmd(projectStatsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"p1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// YAML marshalling lowercases the Go field names (no json tags on
	// the anonymous struct), so the key is "totalissues".
	if !strings.Contains(out, "totalissues: 8") || !strings.Contains(out, "agentname: viktor") {
		t.Errorf("yaml output missing: %q", out)
	}
}
