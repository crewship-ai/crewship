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
category: DEVELOPMENT
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
	if parsed.Meta.Category != "DEVELOPMENT" {
		t.Errorf("category = %q, want %q", parsed.Meta.Category, "DEVELOPMENT")
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
		input string
		want  string
	}{
		{"my skill", "my-skill"},
		{"My Skill", "my-skill"},
		{"GitHub Integration", "github-integration"},
		{"my-skill", "my-skill"},
		{"my_skill", "my_skill"},
		{"  hello world  ", "hello-world"},
		{"hello--world", "hello-world"},
	}
	for _, tt := range tests {
		got := skills.Slugify(tt.input)
		if got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
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

func TestNormalizeSkillURL_GitHubBlobURL(t *testing.T) {
	input := "https://github.com/crewship-ai/skills/blob/main/github/SKILL.md"
	want := "https://raw.githubusercontent.com/crewship-ai/skills/main/github/SKILL.md"

	got, err := skills.NormalizeSkillURL(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("NormalizeSkillURL(%q) = %q, want %q", input, got, want)
	}
}

func TestNormalizeSkillURL_GitHubShorthand(t *testing.T) {
	input := "crewship-ai/skills/github/SKILL.md"
	want := "https://raw.githubusercontent.com/crewship-ai/skills/main/github/SKILL.md"

	got, err := skills.NormalizeSkillURL(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("NormalizeSkillURL(%q) = %q, want %q", input, got, want)
	}
}

func TestNormalizeSkillURL_RawURL(t *testing.T) {
	input := "https://raw.githubusercontent.com/crewship-ai/skills/main/SKILL.md"

	got, err := skills.NormalizeSkillURL(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != input {
		t.Errorf("NormalizeSkillURL(%q) = %q, want unchanged %q", input, got, input)
	}
}

func TestNormalizeSkillURL_ArbitraryHTTPS(t *testing.T) {
	input := "https://example.com/my-skill.md"

	got, err := skills.NormalizeSkillURL(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != input {
		t.Errorf("NormalizeSkillURL(%q) = %q, want unchanged %q", input, got, input)
	}
}

// --- Category validation tests ---

func TestParseSKILLMD_InvalidCategory(t *testing.T) {
	input := "---\nname: test-skill\ncategory: INVALID\n---\n# Test"
	_, err := skills.ParseSKILLMD(input)
	if err == nil {
		t.Fatal("expected error for invalid category")
	}
	if !strings.Contains(err.Error(), "invalid category") {
		t.Errorf("error = %q, want to contain 'invalid category'", err.Error())
	}
}

func TestParseSKILLMD_CategoryNormalization(t *testing.T) {
	input := "---\nname: test-skill\ncategory: coding\n---\n# Test"
	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Meta.Category != "CODING" {
		t.Errorf("category = %q, want %q", parsed.Meta.Category, "CODING")
	}
}

func TestParseSKILLMD_EmptyCategory_NoError(t *testing.T) {
	input := "---\nname: test-skill\n---\n# Test"
	parsed, err := skills.ParseSKILLMD(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Meta.Category != "" {
		t.Errorf("category = %q, want empty", parsed.Meta.Category)
	}
}

// --- SSRF / URL validation tests ---

func TestValidateImportURL_HTTPBlocked(t *testing.T) {
	err := skills.ValidateImportURL(context.Background(),"http://example.com/SKILL.md")
	if err == nil {
		t.Fatal("expected error for HTTP URL")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error = %q, want to mention HTTPS", err.Error())
	}
}

func TestValidateImportURL_LocalhostBlocked(t *testing.T) {
	err := skills.ValidateImportURL(context.Background(),"https://localhost/SKILL.md")
	if err == nil {
		t.Fatal("expected error for localhost URL")
	}
}

func TestValidateImportURL_PrivateIPsBlocked(t *testing.T) {
	tests := []string{
		"https://127.0.0.1/SKILL.md",
		"https://10.0.0.1/SKILL.md",
		"https://172.16.0.1/SKILL.md",
		"https://192.168.1.1/SKILL.md",
		"https://169.254.169.254/latest/meta-data",
	}
	for _, url := range tests {
		if err := skills.ValidateImportURL(context.Background(),url); err == nil {
			t.Errorf("expected error for %q, got nil", url)
		}
	}
}

func TestValidateImportURL_ValidGitHub(t *testing.T) {
	tests := []string{
		"https://raw.githubusercontent.com/org/repo/main/SKILL.md",
		"https://example.com/skills/my-skill.md",
	}
	for _, url := range tests {
		if err := skills.ValidateImportURL(context.Background(),url); err != nil {
			t.Errorf("unexpected error for %q: %v", url, err)
		}
	}
}

func TestValidateImportURL_GitHubBlobConverted(t *testing.T) {
	// GitHub blob URL gets normalized to raw.githubusercontent.com (HTTPS)
	err := skills.ValidateImportURL(context.Background(),"https://github.com/org/repo/blob/main/SKILL.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateImportURL_ShorthandConverted(t *testing.T) {
	// Shorthand gets normalized to raw.githubusercontent.com (HTTPS)
	err := skills.ValidateImportURL(context.Background(),"org/repo/SKILL.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
