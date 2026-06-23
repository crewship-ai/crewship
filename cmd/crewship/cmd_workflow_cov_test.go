package main

// Coverage tests for cmd_workflow.go — the workflow-template CRUD
// commands plus the slug-resolution / manifest-parsing helpers.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covWorkflowManifest = `apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Engineering Standard
  slug: engineering-standard
spec:
  description: Default lifecycle
  icon: hammer
  color: "#3B82F6"
  stages:
    - { name: backlog, type: open, position: 1, color: "#9CA3AF" }
    - { name: done, type: completed, position: 2 }
`

func covWriteManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wt.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// covWorkflowTemplates is the canonical stubbed catalog used by the
// slug-resolution tests.
func covWorkflowTemplates() []map[string]any {
	return []map[string]any{
		{
			"id": "wt1", "name": "Engineering Standard", "is_builtin": false,
			"template_json": `[{"name":"backlog","type":"open","position":1}]`,
		},
		{
			"id": "wt2", "name": "Ops", "is_builtin": true,
			"template_json": `[]`,
		},
	}
}

// ─── helpers: countStages / slugify / marshalStages ──────────────────────

func TestCountStages(t *testing.T) {
	if got := countStages(`[{"name":"a","type":"open","position":1},{"name":"b","type":"done","position":2}]`); got != 2 {
		t.Errorf("countStages valid = %d, want 2", got)
	}
	if got := countStages("not json"); got != 0 {
		t.Errorf("countStages corrupt = %d, want 0", got)
	}
	if got := countStages("[]"); got != 0 {
		t.Errorf("countStages empty = %d, want 0", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Engineering Standard": "engineering-standard",
		"  spaced   out  ":     "spaced-out",
		"under_scored":         "under-scored",
		"MiXeD Case":           "mixed-case",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarshalStages_NoHTMLEscapeNoNewline(t *testing.T) {
	out, err := marshalStages([]workflowManifestStage{
		{Name: "a<b", Type: "open", Position: 1},
	})
	if err != nil {
		t.Fatalf("marshalStages: %v", err)
	}
	if strings.HasSuffix(out, "\n") {
		t.Errorf("trailing newline must be stripped: %q", out)
	}
	if !strings.Contains(out, `"a<b"`) {
		t.Errorf("HTML escaping must be off, got %q", out)
	}
}

// ─── loadWorkflowTemplateBody ────────────────────────────────────────────

func TestLoadWorkflowTemplateBody_Full(t *testing.T) {
	path := covWriteManifest(t, covWorkflowManifest)
	body, err := loadWorkflowTemplateBody(path)
	if err != nil {
		t.Fatalf("loadWorkflowTemplateBody: %v", err)
	}
	if body["name"] != "Engineering Standard" {
		t.Errorf("name = %v", body["name"])
	}
	if body["description"] != "Default lifecycle" || body["icon"] != "hammer" || body["color"] != "#3B82F6" {
		t.Errorf("optional fields wrong: %v", body)
	}
	tj, _ := body["template_json"].(string)
	if countStages(tj) != 2 || !strings.Contains(tj, `"backlog"`) {
		t.Errorf("template_json wrong: %q", tj)
	}
}

func TestLoadWorkflowTemplateBody_OmitsEmptyOptionals(t *testing.T) {
	path := covWriteManifest(t, `kind: WorkflowTemplate
metadata:
  name: Minimal
spec:
  stages:
    - { name: a, type: open, position: 1 }
`)
	body, err := loadWorkflowTemplateBody(path)
	if err != nil {
		t.Fatalf("loadWorkflowTemplateBody: %v", err)
	}
	for _, k := range []string{"description", "icon", "color"} {
		if _, ok := body[k]; ok {
			t.Errorf("empty optional %q must be omitted", k)
		}
	}
}

func TestLoadWorkflowTemplateBody_Errors(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{"wrong kind", "kind: Crew\nmetadata:\n  name: x\nspec:\n  stages:\n    - { name: a, type: open, position: 1 }\n", "expected kind: WorkflowTemplate"},
		{"missing name", "kind: WorkflowTemplate\nspec:\n  stages:\n    - { name: a, type: open, position: 1 }\n", "metadata.name is required"},
		{"no stages", "kind: WorkflowTemplate\nmetadata:\n  name: x\nspec:\n  stages: []\n", "spec.stages must contain at least one stage"},
		{"invalid yaml", "kind: [unclosed", "parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := covWriteManifest(t, tc.manifest)
			_, err := loadWorkflowTemplateBody(path)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadWorkflowTemplateBody_MissingFile(t *testing.T) {
	_, err := loadWorkflowTemplateBody(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("expected read error, got %v", err)
	}
}

// ─── findWorkflowTemplateBySlug ──────────────────────────────────────────

func TestFindWorkflowTemplateBySlug_ExactName(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	got, err := findWorkflowTemplateBySlug(client, "engineering standard") // case-insensitive
	if err != nil {
		t.Fatalf("findWorkflowTemplateBySlug: %v", err)
	}
	if got.ID != "wt1" {
		t.Errorf("ID = %q, want wt1", got.ID)
	}
}

func TestFindWorkflowTemplateBySlug_SlugMatch(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	got, err := findWorkflowTemplateBySlug(client, "engineering-standard")
	if err != nil {
		t.Fatalf("findWorkflowTemplateBySlug: %v", err)
	}
	if got.ID != "wt1" {
		t.Errorf("ID = %q, want wt1", got.ID)
	}
}

func TestFindWorkflowTemplateBySlug_NoMatch(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := findWorkflowTemplateBySlug(client, "ghost")
	if err == nil || !strings.Contains(err.Error(), `no workflow template matches slug "ghost"`) {
		t.Errorf("expected no-match error, got %v", err)
	}
}

func TestFindWorkflowTemplateBySlug_AmbiguousSlug(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, []map[string]any{
		{"id": "wt1", "name": "Foo Bar", "template_json": "[]"},
		{"id": "wt2", "name": "foo_bar", "template_json": "[]"},
	}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := findWorkflowTemplateBySlug(client, "foo-bar")
	if err == nil || !strings.Contains(err.Error(), "ambiguous slug") {
		t.Errorf("expected ambiguity error, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "wt1") {
		t.Errorf("ambiguity error should list candidate ids: %v", err)
	}
}

func TestFindWorkflowTemplateBySlug_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workflow-templates", clitest.ErrorResponse(500, "boom"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := findWorkflowTemplateBySlug(client, "x")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

// ─── workflow list / get ─────────────────────────────────────────────────

func TestWorkflowListRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))

	out, err := covCaptureStdoutCli6(t, func() error {
		return workflowListCmd.RunE(workflowListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Engineering Standard") || !strings.Contains(out, "Ops") {
		t.Errorf("table missing templates: %q", out)
	}
}

func TestWorkflowListRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/workflow-templates", clitest.ErrorResponse(500, "boom"))

	if err := workflowListCmd.RunE(workflowListCmd, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

func TestWorkflowGetRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))

	out, err := covCaptureStdoutCli6(t, func() error {
		return workflowGetCmd.RunE(workflowGetCmd, []string{"engineering-standard"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Engineering Standard") || !strings.Contains(out, "wt1") {
		t.Errorf("detail output missing fields: %q", out)
	}
}

func TestWorkflowGetRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := workflowGetCmd.RunE(workflowGetCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

// ─── workflow create / update / delete ───────────────────────────────────

func TestWorkflowCreateRunE_RequiresFileFlag(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, workflowCreateCmd, "file")

	err := workflowCreateCmd.RunE(workflowCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Errorf("expected file-required error, got %v", err)
	}
}

func TestWorkflowCreateRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowCreateCmd, "file", covWriteManifest(t, covWorkflowManifest))

	stub.OnPost("/api/v1/workflow-templates", clitest.JSONResponse(201, map[string]any{
		"id": "wt9", "name": "Engineering Standard", "template_json": "[]",
	}))

	if err := workflowCreateCmd.RunE(workflowCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/workflow-templates")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["name"] != "Engineering Standard" {
		t.Errorf("name = %v", body["name"])
	}
	tj, _ := body["template_json"].(string)
	if !strings.Contains(tj, `"backlog"`) {
		t.Errorf("template_json not forwarded: %q", tj)
	}
}

func TestWorkflowCreateRunE_BadManifest(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowCreateCmd, "file", covWriteManifest(t, "kind: Nope\n"))

	err := workflowCreateCmd.RunE(workflowCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "expected kind: WorkflowTemplate") {
		t.Errorf("expected kind validation error, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("bad manifest must not reach the server, got %d calls", n)
	}
}

func TestWorkflowUpdateRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowUpdateCmd, "file", covWriteManifest(t, covWorkflowManifest))

	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))
	stub.OnPatch("/api/v1/workflow-templates/wt1", clitest.JSONResponse(200, map[string]any{
		"id": "wt1", "name": "Engineering Standard", "template_json": "[]",
	}))

	if err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"engineering-standard"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/workflow-templates/wt1")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH to wt1, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["name"] != "Engineering Standard" {
		t.Errorf("PATCH body name = %v", body["name"])
	}
}

func TestWorkflowUpdateRunE_SlugMiss(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowUpdateCmd, "file", covWriteManifest(t, covWorkflowManifest))

	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))

	err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "no workflow template matches") {
		t.Errorf("expected slug-miss error, got %v", err)
	}
}

func TestWorkflowDeleteRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowDeleteCmd, "yes", "true")

	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))
	stub.OnDelete("/api/v1/workflow-templates/wt2", clitest.EmptyResponse(204))

	if err := workflowDeleteCmd.RunE(workflowDeleteCmd, []string{"Ops"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(stub.CallsFor("DELETE", "/api/v1/workflow-templates/wt2")); n != 1 {
		t.Errorf("expected exactly 1 DELETE wt2, got %d", n)
	}
}

// ─── shared error-path tables ────────────────────────────────────────────

func workflowRunners(t *testing.T) map[string]func() error {
	t.Helper()
	manifest := covWriteManifest(t, covWorkflowManifest)
	covSetFlagCli6(t, workflowCreateCmd, "file", manifest)
	covSetFlagCli6(t, workflowUpdateCmd, "file", manifest)
	covSetFlagCli6(t, workflowDeleteCmd, "yes", "true")
	return map[string]func() error{
		"list":   func() error { return workflowListCmd.RunE(workflowListCmd, nil) },
		"get":    func() error { return workflowGetCmd.RunE(workflowGetCmd, []string{"x"}) },
		"create": func() error { return workflowCreateCmd.RunE(workflowCreateCmd, nil) },
		"update": func() error { return workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"x"}) },
		"delete": func() error { return workflowDeleteCmd.RunE(workflowDeleteCmd, []string{"x"}) },
	}
}

func TestWorkflowCmds_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	for name, run := range workflowRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", name, err)
		}
	}
}

func TestWorkflowCmds_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	for name, run := range workflowRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", name, err)
		}
	}
}

func TestWorkflowCmds_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close()
	covSetupCli6(t, stub)
	for name, run := range workflowRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "request failed") {
			t.Errorf("%s: expected transport error, got %v", name, err)
		}
	}
}

func TestWorkflowListRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnGet("/api/v1/workflow-templates", clitest.TextResponse(200, "not json"))

	err := workflowListCmd.RunE(workflowListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestWorkflowCreateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowCreateCmd, "file", covWriteManifest(t, covWorkflowManifest))
	stub.OnPost("/api/v1/workflow-templates", clitest.ErrorResponse(409, "name taken"))

	err := workflowCreateCmd.RunE(workflowCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "name taken") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}

func TestWorkflowCreateRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowCreateCmd, "file", covWriteManifest(t, covWorkflowManifest))
	stub.OnPost("/api/v1/workflow-templates", clitest.TextResponse(200, "not json"))

	err := workflowCreateCmd.RunE(workflowCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestWorkflowUpdateRunE_RequiresFileFlag(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, workflowUpdateCmd, "file")

	err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Errorf("expected file-required error, got %v", err)
	}
}

func TestWorkflowUpdateRunE_BadManifest(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowUpdateCmd, "file", covWriteManifest(t, "kind: Nope\n"))

	err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "expected kind: WorkflowTemplate") {
		t.Errorf("expected kind validation error, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("bad manifest must not reach the server, got %d calls", n)
	}
}

func TestWorkflowUpdateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowUpdateCmd, "file", covWriteManifest(t, covWorkflowManifest))
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))
	stub.OnPatch("/api/v1/workflow-templates/wt1", clitest.ErrorResponse(422, "stage list invalid"))

	err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"engineering-standard"})
	if err == nil || !strings.Contains(err.Error(), "stage list invalid") {
		t.Errorf("expected 422 surfaced, got %v", err)
	}
}

func TestWorkflowUpdateRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowUpdateCmd, "file", covWriteManifest(t, covWorkflowManifest))
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))
	stub.OnPatch("/api/v1/workflow-templates/wt1", clitest.TextResponse(200, "not json"))

	err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"engineering-standard"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestWorkflowDeleteRunE_ResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowDeleteCmd, "yes", "true")
	stub.OnGet("/api/v1/workflow-templates", clitest.ErrorResponse(500, "boom"))

	err := workflowDeleteCmd.RunE(workflowDeleteCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected resolve error surfaced, got %v", err)
	}
}

func TestWorkflowDeleteRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, workflowDeleteCmd, "yes", "true")
	stub.OnGet("/api/v1/workflow-templates", clitest.JSONResponse(200, covWorkflowTemplates()))
	stub.OnDelete("/api/v1/workflow-templates/wt2", clitest.ErrorResponse(403, "builtin templates cannot be deleted"))

	err := workflowDeleteCmd.RunE(workflowDeleteCmd, []string{"Ops"})
	if err == nil || !strings.Contains(err.Error(), "builtin templates cannot be deleted") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}

func TestFindWorkflowTemplateBySlug_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close()

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := findWorkflowTemplateBySlug(client, "x")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error, got %v", err)
	}
}

func TestFindWorkflowTemplateBySlug_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workflow-templates", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, err := findWorkflowTemplateBySlug(client, "x")
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestWorkflowDeleteRunE_AbortedWithoutYes(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, workflowDeleteCmd, "yes")

	err := workflowDeleteCmd.RunE(workflowDeleteCmd, []string{"Ops"})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("aborted delete must not issue HTTP calls, got %d", n)
	}
}
