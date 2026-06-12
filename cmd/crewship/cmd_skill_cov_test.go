package main

// Coverage tests for cmd_skill.go — skill list/get/import/create plus the
// assign/unassign fan-out helpers. Uses internal/cli/clitest.StubServer to
// fake the API; tests are deliberately serial (no t.Parallel) because they
// mutate the package-global cliCfg and shared cobra flag state.
//
// This file also hosts the shared cov* helpers used by the other
// *_cov_test.go files in this package.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CUID-shaped fixture IDs (must be 'c' + >=20 lowercase alnum so
// looksLikeCUID short-circuits resolution and the client skips the
// workspace-slug lookup round-trip).
const (
	covWorkspaceIDCli1 = "cworkspace0123456789abcd"
	covAgentIDCli1     = "cagent0123456789abcdefgh"
	covAgent2ID    = "cagent20123456789abcdefg"
	covSkillID     = "cskill0123456789abcdefgh"
	covCrewID      = "ccrew0123456789abcdefghi"
	covCredID      = "ccred0123456789abcdefghi"
	covIntgID      = "cintg0123456789abcdefghi"
)

// covSetup snapshots CLI globals, spins up a stub API server, and points
// cliCfg at it with a valid token + CUID workspace. Returns the stub.
func covSetup(t *testing.T) *clitest.StubServer {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	cliCfg = &cli.CLIConfig{Token: "test-token", Server: s.URL(), Workspace: covWorkspaceIDCli1}
	return s
}

// covSetFlag sets a cobra flag for the duration of the test and restores
// both its value AND its Changed bit on cleanup, so flag state can't leak
// across tests in this shared-command-instance package. Slice flags go
// through pflag.SliceValue.Replace — calling Set twice on a stringSlice
// appends rather than replaces, which would corrupt later tests.
func covSetFlag(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("flag --%s not found on %q", name, cmd.Name())
	}
	origChanged := f.Changed
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		orig := append([]string(nil), sv.GetSlice()...)
		var vals []string
		if value != "" {
			vals = strings.Split(value, ",")
		}
		if err := sv.Replace(vals); err != nil {
			t.Fatalf("replace --%s: %v", name, err)
		}
		f.Changed = true
		t.Cleanup(func() {
			_ = sv.Replace(orig)
			f.Changed = origChanged
		})
		return
	}
	orig := f.Value.String()
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%q: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = f.Value.Set(orig)
		f.Changed = origChanged
	})
}

// covCaptureStdout runs fn with BOTH os.Stdout and os.Stderr redirected to
// one pipe and returns the combined output. Commands in this package split
// their output across the two streams (tables/JSON → stdout, PrintSuccess /
// progress → stderr), and the tests care about content, not stream routing.
// Serial-only (swaps process globals).
func covCaptureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = w, w
	runErr := fn()
	_ = w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	b, _ := io.ReadAll(r)
	_ = r.Close()
	return string(b), runErr
}

// covJSONBody unmarshals a recorded request body into a map.
func covJSONBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal recorded body %q: %v", body, err)
	}
	return m
}

// ─── boolFlag ────────────────────────────────────────────────────────────

func TestBoolFlagCov(t *testing.T) {
	if got := boolFlag(true); got != "1" {
		t.Errorf("boolFlag(true) = %q, want \"1\"", got)
	}
	if got := boolFlag(false); got != "" {
		t.Errorf("boolFlag(false) = %q, want \"\"", got)
	}
}

// ─── resolveSkillID ──────────────────────────────────────────────────────

func TestResolveSkillIDCov_CUIDShortCircuit(t *testing.T) {
	s := covSetup(t)
	got, err := resolveSkillID(newAPIClient(), covSkillID)
	if err != nil {
		t.Fatalf("resolveSkillID: %v", err)
	}
	if got != covSkillID {
		t.Errorf("got %q, want passthrough %q", got, covSkillID)
	}
	if calls := s.Calls(); len(calls) != 0 {
		t.Errorf("CUID input must not hit the API; got %d calls", len(calls))
	}
}

func TestResolveSkillIDCov_SlugLookup(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{
		{"id": "cother0123456789abcdefgh", "slug": "other"},
		{"id": covSkillID, "slug": "pdf-tools"},
	}))
	got, err := resolveSkillID(newAPIClient(), "pdf-tools")
	if err != nil {
		t.Fatalf("resolveSkillID: %v", err)
	}
	if got != covSkillID {
		t.Errorf("got %q, want %q", got, covSkillID)
	}
}

func TestResolveSkillIDCov_NotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{}))
	_, err := resolveSkillID(newAPIClient(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "skill not found: ghost") {
		t.Errorf("want skill-not-found error; got %v", err)
	}
}

func TestResolveSkillIDCov_APIError(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/skills", clitest.ErrorResponse(500, "boom"))
	_, err := resolveSkillID(newAPIClient(), "pdf-tools")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want API error surfaced; got %v", err)
	}
}

// ─── skill list ──────────────────────────────────────────────────────────

func TestSkillListRunECov_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := skillListCmd.RunE(skillListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("want not-logged-in; got %v", err)
	}
}

func TestSkillListRunECov_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := skillListCmd.RunE(skillListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("want workspace error; got %v", err)
	}
}

func TestSkillListRunECov_FiltersAndQuery(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "viktor"},
	}))
	vendor := "anthropic"
	s.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]any{
		{"id": covSkillID, "slug": "pdf", "display_name": "PDF", "category": "CODING",
			"maturity": "OFFICIAL", "source": "BUNDLED", "scan_status": "CLEAN", "vendor": vendor},
		{"id": "cskill20123456789abcdefg", "slug": "bare", "display_name": "Bare", "category": "DATA",
			"maturity": "COMMUNITY", "source": "CUSTOM", "scan_status": "CLEAN", "vendor": nil},
	}))

	covSetFlag(t, skillListCmd, "category", "coding")
	covSetFlag(t, skillListCmd, "maturity", "official")
	covSetFlag(t, skillListCmd, "search", "pdf")
	covSetFlag(t, skillListCmd, "installed", "true")
	covSetFlag(t, skillListCmd, "installed-for", "viktor")

	out, err := covCaptureStdout(t, func() error {
		return skillListCmd.RunE(skillListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "pdf") || !strings.Contains(out, "anthropic") {
		t.Errorf("table output missing expected rows: %q", out)
	}

	skillCalls := s.CallsFor("GET", "/api/v1/skills")
	if len(skillCalls) != 1 {
		t.Fatalf("want 1 GET /api/v1/skills, got %d", len(skillCalls))
	}
	q := skillCalls[0].Query
	for _, want := range []string{
		"category=CODING", "maturity=OFFICIAL", "search=pdf",
		"installed=1", "installed_for_agent_id=" + covAgentIDCli1,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
}

func TestSkillListRunECov_InstalledForResolveFails(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	covSetFlag(t, skillListCmd, "installed-for", "ghost")
	err := skillListCmd.RunE(skillListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve --installed-for agent") {
		t.Errorf("want resolve error; got %v", err)
	}
}

// ─── skill get ───────────────────────────────────────────────────────────

func TestSkillGetRunECov_HappyWithDescription(t *testing.T) {
	s := covSetup(t)
	desc := "Cleans up **PDF** files."
	author := "anthropic"
	tools := 3
	s.OnGet("/api/v1/skills/"+covSkillID, clitest.JSONResponse(200, map[string]any{
		"id": covSkillID, "display_name": "PDF Cleanup", "slug": "pdf-cleanup",
		"category": "CODING", "version": "1.0.0", "source": "BUNDLED",
		"description": desc, "author": author, "tool_count": tools,
		"created_at": "2026-01-01T00:00:00Z",
	}))

	out, err := covCaptureStdout(t, func() error {
		return skillGetCmd.RunE(skillGetCmd, []string{covSkillID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"PDF Cleanup", "pdf-cleanup", "Description:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if len(s.CallsFor("GET", "/api/v1/skills/"+covSkillID)) != 1 {
		t.Error("expected exactly one detail GET")
	}
}

func TestSkillGetRunECov_NotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/skills/"+covSkillID, clitest.ErrorResponse(404, "Skill not found"))
	err := skillGetCmd.RunE(skillGetCmd, []string{covSkillID})
	if err == nil || !strings.Contains(err.Error(), "Skill not found") {
		t.Errorf("want 404 surfaced; got %v", err)
	}
}

// ─── skill import ────────────────────────────────────────────────────────

func TestSkillImportRunECov_NoSource(t *testing.T) {
	covSetup(t)
	err := skillImportCmd.RunE(skillImportCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "provide a URL argument") {
		t.Errorf("want missing-source error; got %v", err)
	}
}

func TestSkillImportRunECov_File(t *testing.T) {
	s := covSetup(t)
	path := filepath.Join(t.TempDir(), "SKILL.md")
	content := "---\nname: x\n---\nbody"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	importPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/import"
	s.OnPost(importPath, clitest.JSONResponse(201, map[string]string{
		"id": covSkillID, "slug": "x", "display_name": "X",
	}))
	covSetFlag(t, skillImportCmd, "file", path)

	if _, err := covCaptureStdout(t, func() error {
		return skillImportCmd.RunE(skillImportCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", importPath)
	if len(calls) != 1 {
		t.Fatalf("want 1 import POST, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["content"] != content {
		t.Errorf("content = %v, want file contents", body["content"])
	}
	if body["source"] != "file" {
		t.Errorf("source = %v, want file", body["source"])
	}
	if body["allow_unsafe_license"] != false {
		t.Errorf("allow_unsafe_license = %v, want false", body["allow_unsafe_license"])
	}
}

func TestSkillImportRunECov_FileReadError(t *testing.T) {
	covSetup(t)
	covSetFlag(t, skillImportCmd, "file", filepath.Join(t.TempDir(), "missing.md"))
	err := skillImportCmd.RunE(skillImportCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "read file") {
		t.Errorf("want read-file error; got %v", err)
	}
}

func TestSkillImportRunECov_URL(t *testing.T) {
	s := covSetup(t)
	importPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/import"
	s.OnPost(importPath, clitest.JSONResponse(201, map[string]string{
		"id": covSkillID, "slug": "remote", "display_name": "Remote",
	}))
	if _, err := covCaptureStdout(t, func() error {
		return skillImportCmd.RunE(skillImportCmd, []string{"https://example.com/SKILL.md"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covJSONBody(t, s.CallsFor("POST", importPath)[0].Body)
	if body["url"] != "https://example.com/SKILL.md" || body["source"] != "url" {
		t.Errorf("body = %v, want url + source=url", body)
	}
}

func TestSkillImportRunECov_Repo(t *testing.T) {
	s := covSetup(t)
	bulkPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/bulk-import"
	s.OnPost(bulkPath, clitest.JSONResponse(200, map[string]any{
		"source": "github.com/foo/bar", "total_found": 2, "total_imported": 1,
		"truncated": false,
		"imported": []map[string]any{
			{"skill_id": covSkillID, "slug": "a", "created": true},
		},
		"skipped": []map[string]any{
			{"path": "skills/b/SKILL.md", "slug": "b", "reason": "license GPL-3.0 not allowed"},
		},
	}))
	covSetFlag(t, skillImportCmd, "repo", "https://github.com/foo/bar")
	covSetFlag(t, skillImportCmd, "ref", "main")
	covSetFlag(t, skillImportCmd, "paths", "skills/*")
	covSetFlag(t, skillImportCmd, "vendor", "community")
	covSetFlag(t, skillImportCmd, "dry-run", "true")

	out, err := covCaptureStdout(t, func() error {
		return skillImportCmd.RunE(skillImportCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Found 2 SKILL.md files; imported 1", "created a", "license GPL-3.0 not allowed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	body := covJSONBody(t, s.CallsFor("POST", bulkPath)[0].Body)
	if body["git_url"] != "https://github.com/foo/bar" || body["git_ref"] != "main" {
		t.Errorf("repo body wrong: %v", body)
	}
	if body["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", body["dry_run"])
	}
	paths, _ := body["paths"].([]any)
	if len(paths) != 1 || paths[0] != "skills/*" {
		t.Errorf("paths = %v, want [skills/*]", body["paths"])
	}
}

func TestSkillImportRunECov_RepoTruncated(t *testing.T) {
	s := covSetup(t)
	bulkPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/bulk-import"
	s.OnPost(bulkPath, clitest.JSONResponse(200, map[string]any{
		"source": "x", "total_found": 500, "total_imported": 500, "truncated": true,
	}))
	covSetFlag(t, skillImportCmd, "repo", "https://github.com/big/repo")
	_, err := covCaptureStdout(t, func() error {
		return skillImportCmd.RunE(skillImportCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "bulk import truncated") {
		t.Errorf("truncated result must exit non-zero; got %v", err)
	}
}

// ─── assign / unassign ───────────────────────────────────────────────────

func covStubSkillAndAgents(s *clitest.StubServer) {
	s.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{
		{"id": covSkillID, "slug": "pdf-tools"},
	}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "viktor", "crew_id": covCrewID},
		{"id": covAgent2ID, "slug": "nela", "crew_id": covCrewID},
		{"id": "cagent30123456789abcdefg", "slug": "outsider", "crew_id": "ccrew20123456789abcdefgh"},
	}))
}

func TestSkillAssignRunECov_Positional(t *testing.T) {
	s := covSetup(t)
	covStubSkillAndAgents(s)
	s.OnPost("/api/v1/agents/"+covAgentIDCli1+"/skills", clitest.JSONResponse(201, map[string]string{"id": "x"}))

	if err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools", "viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli1+"/skills")
	if len(calls) != 1 {
		t.Fatalf("want 1 assign POST, got %d", len(calls))
	}
	if body := covJSONBody(t, calls[0].Body); body["skill_id"] != covSkillID {
		t.Errorf("skill_id = %v, want %q", body["skill_id"], covSkillID)
	}
}

func TestSkillAssignRunECov_ToCrewFanout(t *testing.T) {
	s := covSetup(t)
	covStubSkillAndAgents(s)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewID, "slug": "engineering"},
	}))
	s.OnPost("/api/v1/agents/"+covAgentIDCli1+"/skills", clitest.JSONResponse(201, map[string]string{"id": "x"}))
	s.OnPost("/api/v1/agents/"+covAgent2ID+"/skills", clitest.JSONResponse(201, map[string]string{"id": "y"}))
	covSetFlag(t, skillAssignCmd, "to-crew", "engineering")

	if err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Exactly the two crew members — the outsider agent must NOT be hit.
	if n := len(s.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli1+"/skills")); n != 1 {
		t.Errorf("viktor assign calls = %d, want 1", n)
	}
	if n := len(s.CallsFor("POST", "/api/v1/agents/"+covAgent2ID+"/skills")); n != 1 {
		t.Errorf("nela assign calls = %d, want 1", n)
	}
	if n := len(s.CallsFor("POST", "/api/v1/agents/cagent30123456789abcdefg/skills")); n != 0 {
		t.Errorf("outsider agent must not be touched; got %d calls", n)
	}
}

func TestSkillAssignRunECov_ConflictingTargets(t *testing.T) {
	s := covSetup(t)
	covStubSkillAndAgents(s)
	covSetFlag(t, skillAssignCmd, "to-agents", "viktor,nela")
	err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools", "viktor"})
	if err == nil || !strings.Contains(err.Error(), "pick one of") {
		t.Errorf("want ambiguity rejection; got %v", err)
	}
}

func TestSkillAssignRunECov_NoTarget(t *testing.T) {
	s := covSetup(t)
	covStubSkillAndAgents(s)
	err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools"})
	if err == nil || !strings.Contains(err.Error(), "specify an agent") {
		t.Errorf("want no-target error; got %v", err)
	}
}

func TestSkillUnassignRunECov_ToAgentsPartialFailure(t *testing.T) {
	s := covSetup(t)
	covStubSkillAndAgents(s)
	s.OnDelete("/api/v1/agents/"+covAgentIDCli1+"/skills/"+covSkillID,
		clitest.JSONResponse(200, map[string]string{}))
	s.OnDelete("/api/v1/agents/"+covAgent2ID+"/skills/"+covSkillID,
		clitest.ErrorResponse(404, "not assigned"))
	covSetFlag(t, skillUnassignCmd, "to-agents", "viktor,nela")

	err := skillUnassignCmd.RunE(skillUnassignCmd, []string{"pdf-tools"})
	if err == nil || !strings.Contains(err.Error(), "unassign failed for 1 of 2 agents") {
		t.Errorf("want partial-failure summary; got %v", err)
	}
	// Both deletes must have been attempted — failures collect, not abort.
	if n := len(s.CallsFor("DELETE", "/api/v1/agents/"+covAgent2ID+"/skills/"+covSkillID)); n != 1 {
		t.Errorf("failing agent delete attempts = %d, want 1", n)
	}
}

func TestResolveAssignTargetsCov_EmptyToAgents(t *testing.T) {
	covSetup(t)
	_, err := resolveAssignTargets(newAPIClient(), []string{"skill"}, []string{" ", ""}, "")
	if err == nil || !strings.Contains(err.Error(), "--to-agents was empty after parsing") {
		t.Errorf("want empty-list error; got %v", err)
	}
}

func TestResolveCrewMembersCov_EmptyCrew(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewID, "slug": "lonely"},
	}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "viktor", "crew_id": "ccrew20123456789abcdefgh"},
	}))
	_, err := resolveCrewMembers(newAPIClient(), "lonely")
	if err == nil || !strings.Contains(err.Error(), `crew "lonely" has no agents`) {
		t.Errorf("want no-agents error; got %v", err)
	}
}

// ─── skill create ────────────────────────────────────────────────────────

func TestSkillCreateRunECov_MissingFlags(t *testing.T) {
	covSetup(t)
	err := skillCreateCmd.RunE(skillCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--slug and --prompt are required") {
		t.Errorf("want required-flags error; got %v", err)
	}
}

func TestSkillCreateRunECov_PrintMode(t *testing.T) {
	s := covSetup(t)
	genPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/generate"
	s.OnPost(genPath, clitest.JSONResponse(200, map[string]string{
		"skill_id": covSkillID, "slug": "pdf-cleanup",
		"content": "---\nname: pdf-cleanup\n---\ngenerated body",
		"scan_status": "CLEAN",
	}))
	covSetFlag(t, skillCreateCmd, "slug", "pdf-cleanup")
	covSetFlag(t, skillCreateCmd, "prompt", "sanitise PDFs")
	covSetFlag(t, skillCreateCmd, "model", "claude-sonnet-4-6")
	covSetFlag(t, skillCreateCmd, "print", "true")

	out, err := covCaptureStdout(t, func() error {
		return skillCreateCmd.RunE(skillCreateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "generated body") {
		t.Errorf("--print must dump content; got %q", out)
	}
	body := covJSONBody(t, s.CallsFor("POST", genPath)[0].Body)
	if body["slug"] != "pdf-cleanup" || body["prompt"] != "sanitise PDFs" || body["model"] != "claude-sonnet-4-6" {
		t.Errorf("generate body wrong: %v", body)
	}
}

func TestSkillCreateRunECov_FlaggedSummary(t *testing.T) {
	s := covSetup(t)
	genPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/generate"
	s.OnPost(genPath, clitest.JSONResponse(200, map[string]string{
		"skill_id": covSkillID, "slug": "risky",
		"content": "x", "scan_status": "FLAGGED", "scan_reason": "embedded curl|bash",
		"description_quality": "poor",
	}))
	covSetFlag(t, skillCreateCmd, "slug", "risky")
	covSetFlag(t, skillCreateCmd, "prompt", "do things")

	if _, err := covCaptureStdout(t, func() error {
		return skillCreateCmd.RunE(skillCreateCmd, nil)
	}); err != nil {
		t.Fatalf("FLAGGED result is informational, not an error: %v", err)
	}
}
