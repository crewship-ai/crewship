package skills_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
)

func TestParseSKILLMD_ValidFull(t *testing.T) {
	input := `---
name: github-integration
display_name: GitHub Integration
version: 1.0.0
author: crewship-ai
description: Work with GitHub repos, PRs, issues, and code reviews.
category: DEVOPS
icon: git-pull-request
credential_requirements:
  - GITHUB_TOKEN
tags:
  - github
  - code-review
---
# GitHub Integration

## Role & Persona
You are a GitHub expert.

## Instructions
Use GitHub API for all operations.`

	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Meta.Name != "github-integration" {
		t.Errorf("name = %q, want %q", parsed.Meta.Name, "github-integration")
	}
	if parsed.Meta.DisplayName != "GitHub Integration" {
		t.Errorf("display_name = %q, want %q", parsed.Meta.DisplayName, "GitHub Integration")
	}
	if parsed.Meta.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", parsed.Meta.Version, "1.0.0")
	}
	if parsed.Meta.Author != "crewship-ai" {
		t.Errorf("author = %q, want %q", parsed.Meta.Author, "crewship-ai")
	}
	if parsed.Meta.Category != "DEVOPS" {
		t.Errorf("category = %q, want %q", parsed.Meta.Category, "DEVOPS")
	}
	if parsed.Meta.Icon != "git-pull-request" {
		t.Errorf("icon = %q, want %q", parsed.Meta.Icon, "git-pull-request")
	}
	if len(parsed.Meta.CredentialRequirements) != 1 || parsed.Meta.CredentialRequirements[0] != "GITHUB_TOKEN" {
		t.Errorf("credential_requirements = %v, want [GITHUB_TOKEN]", parsed.Meta.CredentialRequirements)
	}
	if len(parsed.Meta.Tags) != 2 {
		t.Errorf("tags length = %d, want 2", len(parsed.Meta.Tags))
	}
	if !strings.Contains(parsed.Content, "# GitHub Integration") {
		t.Errorf("content missing '# GitHub Integration', got: %s", parsed.Content)
	}
}

func TestParseSKILLMD_MinimalOnlyName(t *testing.T) {
	input := `---
name: my-skill
---
# My Skill
Just a simple skill.`

	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Meta.Name != "my-skill" {
		t.Errorf("name = %q, want %q", parsed.Meta.Name, "my-skill")
	}
	if parsed.Meta.Version != "" {
		t.Errorf("version = %q, want empty (caller sets default)", parsed.Meta.Version)
	}
	if parsed.Meta.Category != "" {
		t.Errorf("category = %q, want empty (caller sets default)", parsed.Meta.Category)
	}
	if len(parsed.Meta.CredentialRequirements) != 0 {
		t.Errorf("credential_requirements = %v, want empty", parsed.Meta.CredentialRequirements)
	}
	if !strings.Contains(parsed.Content, "# My Skill") {
		t.Errorf("content missing '# My Skill', got: %s", parsed.Content)
	}
}

func TestParseSKILLMD_MissingFrontmatter(t *testing.T) {
	input := `# My Skill
No frontmatter here.`

	_, err := skills.ParseSKILLMD(input)
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
	if !strings.Contains(err.Error(), "no YAML frontmatter") {
		t.Errorf("error = %q, want to contain 'no YAML frontmatter'", err.Error())
	}
}

func TestParseSKILLMD_EmptyName(t *testing.T) {
	input := `---
display_name: Missing Name Skill
---
# Content`

	_, err := skills.ParseSKILLMD(input)
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error = %q, want to contain 'name is required'", err.Error())
	}
}

func TestParseSKILLMD_SlugGeneration(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"spaces_to_hyphens", "my skill", "my-skill"},
		{"uppercase_lowered", "My Skill", "my-skill"},
		{"camel_case", "GitHub Integration", "github-integration"},
		{"already_slug", "my-skill", "my-skill"},
		{"underscores_kept", "my_skill", "my_skill"},
		{"trim_whitespace", "  hello world  ", "hello-world"},
		{"collapse_hyphens", "hello--world", "hello-world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := skills.Slugify(tt.input)
			if got != tt.want {
				t.Fatalf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSKILLMD_CredentialList(t *testing.T) {
	input := `---
name: multi-cred-skill
credential_requirements:
  - GITHUB_TOKEN
  - SLACK_API_KEY
  - JIRA_TOKEN
---
# Multi Credential Skill`

	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.Meta.CredentialRequirements) != 3 {
		t.Errorf("credential_requirements length = %d, want 3", len(parsed.Meta.CredentialRequirements))
	}
	wantCreds := []string{"GITHUB_TOKEN", "SLACK_API_KEY", "JIRA_TOKEN"}
	for i, want := range wantCreds {
		if parsed.Meta.CredentialRequirements[i] != want {
			t.Errorf("credential_requirements[%d] = %q, want %q", i, parsed.Meta.CredentialRequirements[i], want)
		}
	}
}

func TestParseSKILLMD_ContentExtracted(t *testing.T) {
	input := `---
name: content-test
---
# Title
Some content here.
## Section
More content.
`

	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(parsed.Content, "# Title") {
		t.Error("content missing '# Title'")
	}
	if !strings.Contains(parsed.Content, "## Section") {
		t.Error("content missing '## Section'")
	}
	// Content should not include frontmatter
	if strings.Contains(parsed.Content, "name: content-test") {
		t.Error("content should not contain frontmatter")
	}
}

func TestNormalizeSkillURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"github_blob", "https://github.com/crewship-ai/skills/blob/main/github/SKILL.md", "https://raw.githubusercontent.com/crewship-ai/skills/main/github/SKILL.md"},
		{"github_shorthand", "crewship-ai/skills/github/SKILL.md", "https://raw.githubusercontent.com/crewship-ai/skills/main/github/SKILL.md"},
		{"raw_url_passthrough", "https://raw.githubusercontent.com/crewship-ai/skills/main/SKILL.md", "https://raw.githubusercontent.com/crewship-ai/skills/main/SKILL.md"},
		{"arbitrary_https_passthrough", "https://example.com/my-skill.md", "https://example.com/my-skill.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := skills.NormalizeSkillURL(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("NormalizeSkillURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSKILLMD_Category(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantErr      bool
		wantContains string
		wantCategory string
	}{
		// v65: unknown categories fall back to CUSTOM rather than rejecting
		// the whole skill. Third-party registries use a long tail of category
		// strings; a hard reject would block ~30% of imports.
		{"invalid_category_falls_back", "---\nname: test-skill\ncategory: INVALID\n---\n# Test", false, "", "CUSTOM"},
		{"normalization", "---\nname: test-skill\ncategory: coding\n---\n# Test", false, "", "CODING"},
		{"empty_allowed", "---\nname: test-skill\n---\n# Test", false, "", ""},
		{"new_writing_category", "---\nname: test-skill\ncategory: writing\n---\n# Test", false, "", "WRITING"},
		{"new_security_category", "---\nname: test-skill\ncategory: security\n---\n# Test", false, "", "SECURITY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := skills.ParseSKILLMD(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if parsed.Meta.Category != tt.wantCategory {
				t.Errorf("category = %q, want %q", parsed.Meta.Category, tt.wantCategory)
			}
		})
	}
}

func TestParseSKILLMD_StripsDynamicContext(t *testing.T) {
	t.Parallel()
	// Anthropic Claude Code expands !`...` by shelling out at load time.
	// Crewship strips it on import — multi-tenant containers can't trust
	// arbitrary shell from third-party SKILL.md.
	input := "---\nname: test-skill\n---\n## Diff\n\n!`git diff HEAD`\n\nReview above and summarise."
	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(parsed.Content, "!`") {
		t.Errorf("dynamic context not stripped: %q", parsed.Content)
	}
	if !strings.Contains(parsed.Content, "Review above") {
		t.Errorf("body content lost during strip: %q", parsed.Content)
	}
}

func TestLintDescription(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want bool // true => passes (returns "")
	}{
		{"missing", "", false},
		{"too_short", "Helps with PDFs", false},
		{"no_trigger_phrase", "Performs operations on documents and files of various kinds today.", false},
		{"use_when", "Use when the user asks to extract tables from a PDF document.", true},
		{"use_this_skill_when", "Use this skill when reviewing SQL migrations for safety.", true},
		{"useful_for", "Useful for triaging Kubernetes pod failures and CrashLoopBackOff incidents.", true},
		{"to_verb", "To extract structured data from PDF documents using OCR layout analysis.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := skills.LintDescription(tt.desc)
			passed := got == ""
			if passed != tt.want {
				t.Errorf("LintDescription(%q) = %q, want pass=%v", tt.desc, got, tt.want)
			}
		})
	}
}

func TestValidateImportURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		wantErr      bool
		wantContains string
	}{
		{"http_blocked", "http://example.com/SKILL.md", true, "HTTPS"},
		{"localhost_blocked", "https://localhost/SKILL.md", true, "localhost"},
		{"loopback_blocked", "https://127.0.0.1/SKILL.md", true, "private"},
		{"private_10_blocked", "https://10.0.0.1/SKILL.md", true, "private"},
		{"private_172_blocked", "https://172.16.0.1/SKILL.md", true, "private"},
		{"private_192_blocked", "https://192.168.1.1/SKILL.md", true, "private"},
		{"link_local_blocked", "https://169.254.169.254/latest/meta-data", true, "private"},
		{"valid_raw_github", "https://raw.githubusercontent.com/org/repo/main/SKILL.md", false, ""},
		{"valid_arbitrary", "https://example.com/skills/my-skill.md", false, ""},
		{"github_blob_converted", "https://github.com/org/repo/blob/main/SKILL.md", false, ""},
		{"shorthand_converted", "org/repo/SKILL.md", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := skills.ValidateImportURL(context.Background(), tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tt.url)
				}
				if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.url, err)
			}
		})
	}
}
