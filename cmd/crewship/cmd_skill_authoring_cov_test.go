package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// skillRow mirrors the anonymous struct assembleSkillMD takes — identical
// field names, types and tags, so Go treats them as the same type.
type skillRowAlias = struct {
	Slug                   string  `json:"slug"`
	DisplayName            string  `json:"display_name"`
	Description            *string `json:"description"`
	Version                string  `json:"version"`
	Author                 *string `json:"author"`
	Vendor                 *string `json:"vendor"`
	Homepage               *string `json:"homepage"`
	License                *string `json:"license"`
	Category               string  `json:"category"`
	Runtime                string  `json:"runtime"`
	Maturity               string  `json:"maturity"`
	Icon                   *string `json:"icon"`
	Tags                   *string `json:"tags"`
	CredentialRequirements *string `json:"credential_requirements"`
	Content                *string `json:"content"`
}

func covStrPtr(s string) *string { return &s }

// ─── pure helpers ────────────────────────────────────────────────────────

func TestValidSlug(t *testing.T) {
	t.Parallel()
	cases := []struct {
		slug string
		want bool
	}{
		{"pdf-cleanup", true},
		{"a", true},
		{"0skill", true},
		{"dot.and_under-dash", true},
		{"", false},
		{"UPPER", false},
		{"-leading-dash", false},
		{"has space", false},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
	}
	for _, tc := range cases {
		if got := validSlug(tc.slug); got != tc.want {
			t.Errorf("validSlug(%q) = %v, want %v", tc.slug, got, tc.want)
		}
	}
}

func TestValidCategory(t *testing.T) {
	t.Parallel()
	for _, c := range validCategories() {
		if !validCategory(c) {
			t.Errorf("validCategory(%q) = false for a canonical category", c)
		}
	}
	for _, c := range []string{"", "coding", "BOGUS"} {
		if validCategory(c) {
			t.Errorf("validCategory(%q) = true, want false", c)
		}
	}
	if len(validCategories()) != 14 {
		t.Errorf("validCategories() has %d entries, want 14", len(validCategories()))
	}
}

func TestDecodeStringList(t *testing.T) {
	t.Parallel()
	if got := decodeStringList(nil); got != nil {
		t.Errorf("nil input: got %v", got)
	}
	empty := ""
	if got := decodeStringList(&empty); got != nil {
		t.Errorf("empty input: got %v", got)
	}
	emptyList := "[]"
	if got := decodeStringList(&emptyList); got != nil {
		t.Errorf("[] input: got %v", got)
	}
	bad := "{not json"
	if got := decodeStringList(&bad); got != nil {
		t.Errorf("invalid json: got %v", got)
	}
	good := `["a","b"]`
	got := decodeStringList(&good)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("valid json: got %v, want [a b]", got)
	}
}

func TestYamlScalar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", `""`},
		{"plain-tag", "plain-tag"},
		{" leading-space", `" leading-space"`},
		{"true", `"true"`},
		{"NULL", `"NULL"`},
		{"pdf:cleanup", `"pdf:cleanup"`},
		{"has\nnewline", `"has\nnewline"`},
	}
	for _, tc := range cases {
		if got := yamlScalar(tc.in); got != tc.want {
			t.Errorf("yamlScalar(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteYAMLList(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writeYAMLList(&b, "tags", []string{"clean", "x:y"})
	want := "tags:\n  - clean\n  - \"x:y\"\n"
	if b.String() != want {
		t.Errorf("writeYAMLList = %q, want %q", b.String(), want)
	}
}

func TestJsonQuote(t *testing.T) {
	t.Parallel()
	if got := jsonQuote(`a"b`); got != `"a\"b"` {
		t.Errorf("jsonQuote = %q", got)
	}
}

func TestBuildSkillScaffold(t *testing.T) {
	t.Parallel()
	out := buildSkillScaffold("my-skill", "DEVOPS", "Use when testing", "Apache-2.0")
	for _, want := range []string{
		"name: my-skill",
		"description: Use when testing",
		"license: Apache-2.0",
		"category: DEVOPS",
		"runtime: INSTRUCTIONS",
		"maturity: COMMUNITY",
		"## When to use",
		"## Steps",
		"## Output format",
		"## Guardrails",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scaffold missing %q", want)
		}
	}
}

// ─── assembleSkillMD ─────────────────────────────────────────────────────

func TestAssembleSkillMD_FullFrontmatter(t *testing.T) {
	t.Parallel()
	s := skillRowAlias{
		Slug:                   "my-skill",
		DisplayName:            "My Skill",
		Description:            covStrPtr("Use when assembling"),
		Version:                "2.0.0",
		Author:                 covStrPtr("Pavel"),
		Vendor:                 covStrPtr("Crewship"),
		Homepage:               covStrPtr("https://example.com"),
		License:                covStrPtr("MIT"),
		Category:               "DEVOPS",
		Runtime:                "MCP",
		Maturity:               "OFFICIAL",
		Icon:                   covStrPtr("🛠"),
		Tags:                   covStrPtr(`["a","b:c"]`),
		CredentialRequirements: covStrPtr(`["GITHUB_TOKEN"]`),
		Content:                covStrPtr("## Body\n\nstuff\n\n\n"),
	}
	out := assembleSkillMD(s)
	for _, want := range []string{
		"name: my-skill\n",
		"display_name: My Skill\n",
		"description: Use when assembling\n",
		"license: MIT\n",
		"vendor: Crewship\n",
		"homepage: https://example.com\n",
		"version: 2.0.0\n",
		"author: Pavel\n",
		"category: DEVOPS\n",
		"runtime: MCP\n",
		"maturity: OFFICIAL\n",
		"icon: 🛠\n",
		"tags:\n  - a\n  - \"b:c\"\n",
		"credential_requirements:\n  - GITHUB_TOKEN\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("assembled SKILL.md missing %q; got:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "## Body\n\nstuff\n") {
		t.Errorf("body not trimmed to single trailing newline; got:\n%q", out)
	}
}

func TestAssembleSkillMD_DefaultsSkipped(t *testing.T) {
	t.Parallel()
	s := skillRowAlias{
		Slug:        "min",
		DisplayName: "min", // equals slug → skipped
		Version:     "1.0.0",
		Category:    "CUSTOM",
		Runtime:     "INSTRUCTIONS",
		Maturity:    "COMMUNITY",
		Content:     covStrPtr("body"),
	}
	out := assembleSkillMD(s)
	for _, absent := range []string{"display_name:", "version:", "runtime:", "maturity:", "description:", "tags:"} {
		if strings.Contains(out, absent) {
			t.Errorf("default-valued field %q should be omitted; got:\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "name: min\n") || !strings.Contains(out, "category: CUSTOM\n") {
		t.Errorf("required fields missing; got:\n%s", out)
	}
}

// ─── skill init ──────────────────────────────────────────────────────────

func TestSkillInitRunE_InvalidSlug(t *testing.T) {
	covSetupCli5(t)
	err := skillInitCmd.RunE(skillInitCmd, []string{"Bad Slug"})
	if err == nil || !strings.Contains(err.Error(), "invalid slug") {
		t.Errorf("expected invalid-slug error; got %v", err)
	}
}

func TestSkillInitRunE_InvalidCategory(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, skillInitCmd, "category", "NOPE")
	err := skillInitCmd.RunE(skillInitCmd, []string{"ok-slug"})
	if err == nil || !strings.Contains(err.Error(), "invalid category") {
		t.Errorf("expected invalid-category error; got %v", err)
	}
}

func TestSkillInitRunE_WritesScaffold(t *testing.T) {
	covSetupCli5(t)
	dir := t.TempDir()
	covSetFlagCli5(t, skillInitCmd, "output", dir)
	covSetFlagCli5(t, skillInitCmd, "category", "data") // lowercase → upcased
	covSetFlagCli5(t, skillInitCmd, "description", "Use when seeding demo data")
	covSetFlagCli5(t, skillInitCmd, "license", "BSD-3-Clause")

	if err := skillInitCmd.RunE(skillInitCmd, []string{"demo-seeder"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("scaffold not written: %v", err)
	}
	content := string(data)
	for _, want := range []string{"name: demo-seeder", "category: DATA", "description: Use when seeding demo data", "license: BSD-3-Clause"} {
		if !strings.Contains(content, want) {
			t.Errorf("scaffold missing %q", want)
		}
	}

	// Second run without --force must refuse to overwrite.
	err = skillInitCmd.RunE(skillInitCmd, []string{"demo-seeder"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("expected overwrite refusal; got %v", err)
	}

	// --force overwrites.
	covSetFlagCli5(t, skillInitCmd, "force", "true")
	if err := skillInitCmd.RunE(skillInitCmd, []string{"demo-seeder"}); err != nil {
		t.Errorf("RunE with --force: %v", err)
	}
}

// ─── skill export ────────────────────────────────────────────────────────

const covSkillIDCli5 = "cskill00000000000000000"

func covSkillPayload(content string) map[string]any {
	return map[string]any{
		"slug":         "my-skill",
		"display_name": "My Skill",
		"description":  "Use when exporting",
		"version":      "1.0.0",
		"license":      "MIT",
		"category":     "CUSTOM",
		"runtime":      "INSTRUCTIONS",
		"maturity":     "COMMUNITY",
		"content":      content,
	}
}

func TestSkillExportRunE_Stdout(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills/"+covSkillIDCli5,
		clitest.JSONResponse(200, covSkillPayload("## When to use\n\nexported body")))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = skillExportCmd.RunE(skillExportCmd, []string{covSkillIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"name: my-skill", "description: Use when exporting", "exported body"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout export missing %q; got:\n%s", want, out)
		}
	}
	calls := stub.CallsFor("GET", "/api/v1/skills/"+covSkillIDCli5)
	if len(calls) != 1 {
		t.Fatalf("expected 1 GET skill detail, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "workspace_id="+covWSCli5) {
		t.Errorf("detail query missing workspace_id: %q", calls[0].Query)
	}
}

func TestSkillExportRunE_ToDirectory(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills/"+covSkillIDCli5,
		clitest.JSONResponse(200, covSkillPayload("body here")))
	dir := t.TempDir()
	covSetFlagCli5(t, skillExportCmd, "output", dir)

	if err := skillExportCmd.RunE(skillExportCmd, []string{covSkillIDCli5}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "my-skill.md"))
	if err != nil {
		t.Fatalf("expected <slug>.md in output dir: %v", err)
	}
	if !strings.Contains(string(data), "body here") {
		t.Errorf("written file missing content; got:\n%s", string(data))
	}
}

func TestSkillExportRunE_SlugResolution(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills", clitest.JSONResponse(200,
		[]map[string]string{{"id": covSkillIDCli5, "slug": "my-skill"}}))
	stub.OnGet("/api/v1/skills/"+covSkillIDCli5,
		clitest.JSONResponse(200, covSkillPayload("via slug")))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = skillExportCmd.RunE(skillExportCmd, []string{"my-skill"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "via slug") {
		t.Errorf("slug-resolved export missing content; got:\n%s", out)
	}
}

func TestSkillExportRunE_NoContent(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills/"+covSkillIDCli5,
		clitest.JSONResponse(200, covSkillPayload("")))

	err := skillExportCmd.RunE(skillExportCmd, []string{covSkillIDCli5})
	if err == nil || !strings.Contains(err.Error(), "no content") {
		t.Errorf("expected no-content error; got %v", err)
	}
}

func TestSkillExportRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := skillExportCmd.RunE(skillExportCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

// ─── skill delete ────────────────────────────────────────────────────────

func TestSkillDeleteRunE_Force(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, skillDeleteCmd, "force", "true")
	delPath := "/api/v1/workspaces/" + covWSCli5 + "/skills/" + covSkillIDCli5
	stub.OnDelete(delPath, clitest.EmptyResponse(204))

	if err := skillDeleteCmd.RunE(skillDeleteCmd, []string{covSkillIDCli5}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", delPath); len(calls) != 1 {
		t.Errorf("expected exactly 1 DELETE %s, got %d", delPath, len(calls))
	}
}

func TestSkillDeleteRunE_PromptAborts(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, skillDeleteCmd, "force", "false")
	covSwapStdin(t, "n\n")

	err := skillDeleteCmd.RunE(skillDeleteCmd, []string{covSkillIDCli5})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected aborted; got %v", err)
	}
}

func TestSkillDeleteRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, skillDeleteCmd, "force", "true")
	stub.OnDelete("/api/v1/workspaces/"+covWSCli5+"/skills/"+covSkillIDCli5,
		clitest.ErrorResponse(404, "skill not found"))

	err := skillDeleteCmd.RunE(skillDeleteCmd, []string{covSkillIDCli5})
	if err == nil || !strings.Contains(err.Error(), "skill not found") {
		t.Errorf("expected API error; got %v", err)
	}
}

// ─── round 2: defaults + remaining error branches ────────────────────────

func TestSkillInitRunE_AllDefaults(t *testing.T) {
	covSetupCli5(t)
	t.Chdir(t.TempDir())
	// Empty category / description / license / output exercise every
	// default-fallback branch; output dir defaults to ./<slug>/.
	covSetFlagCli5(t, skillInitCmd, "category", "")
	covSetFlagCli5(t, skillInitCmd, "description", "")
	covSetFlagCli5(t, skillInitCmd, "license", "")
	covSetFlagCli5(t, skillInitCmd, "output", "")

	if err := skillInitCmd.RunE(skillInitCmd, []string{"defaults-skill"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("defaults-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("scaffold not written to ./<slug>/: %v", err)
	}
	content := string(data)
	for _, want := range []string{"category: CUSTOM", "license: MIT", "TODO: replace with a one-line trigger"} {
		if !strings.Contains(content, want) {
			t.Errorf("default scaffold missing %q", want)
		}
	}
}

func TestSkillInitRunE_WriteFailure(t *testing.T) {
	covSetupCli5(t)
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	covSetFlagCli5(t, skillInitCmd, "output", dir)

	err := skillInitCmd.RunE(skillInitCmd, []string{"cannot-write"})
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Errorf("expected write error; got %v", err)
	}
}

func TestSkillExportRunE_ErrorBranches(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	if err := skillExportCmd.RunE(skillExportCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}

	// Slug that resolves to nothing.
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{}))
	if err := skillExportCmd.RunE(skillExportCmd, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "skill not found") {
		t.Errorf("expected skill-not-found; got %v", err)
	}

	// Detail endpoint 404s.
	stub2 := covSetupCli5(t)
	stub2.OnGet("/api/v1/skills/"+covSkillIDCli5, clitest.ErrorResponse(404, "skill gone"))
	if err := skillExportCmd.RunE(skillExportCmd, []string{covSkillIDCli5}); err == nil || !strings.Contains(err.Error(), "skill gone") {
		t.Errorf("expected 404; got %v", err)
	}

	// Detail body malformed.
	stub3 := covSetupCli5(t)
	stub3.OnGet("/api/v1/skills/"+covSkillIDCli5, clitest.TextResponse(200, "not json"))
	if err := skillExportCmd.RunE(skillExportCmd, []string{covSkillIDCli5}); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSkillExportRunE_WriteFailure(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills/"+covSkillIDCli5, clitest.JSONResponse(200, covSkillPayload("body")))
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	covSetFlagCli5(t, skillExportCmd, "output", dir)

	err := skillExportCmd.RunE(skillExportCmd, []string{covSkillIDCli5})
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Errorf("expected write error; got %v", err)
	}
}

func TestSkillDeleteRunE_GatesAndResolve(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := skillDeleteCmd.RunE(skillDeleteCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	if err := skillDeleteCmd.RunE(skillDeleteCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}

	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/skills", clitest.JSONResponse(200, []map[string]string{}))
	if err := skillDeleteCmd.RunE(skillDeleteCmd, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "skill not found") {
		t.Errorf("expected skill-not-found; got %v", err)
	}
}
