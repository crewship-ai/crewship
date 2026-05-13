package cli

import (
	"strings"
	"testing"
)

func TestParseSlashReader_Frontmatter(t *testing.T) {
	src := `---
name: review
description: Review a diff
agent: viktor
plan: true
vars:
  - target
  - tone
---
Review this ${target} in a ${tone} tone:

$args
`
	sc, err := parseSlashReader(strings.NewReader(src), "/tmp/review.md")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "review" {
		t.Errorf("name=%s", sc.Name)
	}
	if sc.Agent != "viktor" {
		t.Errorf("agent=%s", sc.Agent)
	}
	if !sc.Plan {
		t.Error("plan should be true")
	}
	if len(sc.Vars) != 2 || sc.Vars[0] != "target" {
		t.Errorf("vars=%v", sc.Vars)
	}
	if !strings.Contains(sc.Body, "${target}") {
		t.Errorf("body missing target placeholder")
	}
}

func TestParseSlashReader_NoFrontmatter(t *testing.T) {
	src := "Just plain prompt body"
	sc, err := parseSlashReader(strings.NewReader(src), "/tmp/plain.md")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "plain" {
		t.Errorf("name=%s", sc.Name)
	}
	if sc.Body != "Just plain prompt body" {
		t.Errorf("body=%q", sc.Body)
	}
}

func TestSlashCommand_Render_Args(t *testing.T) {
	sc := SlashCommand{
		Body: "Review the ${target} in a ${tone} tone.\nExtras: $args",
		Vars: []string{"target", "tone"},
	}
	out := sc.Render([]string{"diff", "terse"})
	if !strings.Contains(out, "Review the diff in a terse tone.") {
		t.Errorf("output: %s", out)
	}
}

func TestSlashCommand_Render_ImplicitArgs(t *testing.T) {
	sc := SlashCommand{
		Body: "Summarise $args",
	}
	out := sc.Render([]string{"yesterday", "morning"})
	if out != "Summarise yesterday morning" {
		t.Errorf("output=%q", out)
	}
}

func TestSlashNameValid(t *testing.T) {
	cases := map[string]bool{
		"review":    true,
		"do-thing":  true,
		"r12":       true,
		"":          false,
		"-bad":      false,
		"Foo":       false,
		"rm -rf":    false,
		"plan/act":  false,
	}
	for in, want := range cases {
		if got := slashNameValid(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}
